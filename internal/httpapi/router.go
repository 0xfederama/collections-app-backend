// Package httpapi is the composition root of the HTTP layer: it builds the Huma
// API over chi and registers every feature's operations, plus the health probes.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/favicons"
	"github.com/0xfederama/collections-app-backend/internal/folders"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/logs"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/stores"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/tree"
	"github.com/0xfederama/collections-app-backend/internal/users"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// Pinger is satisfied by *pgxpool.Pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators the router needs.
type Deps struct {
	DB      Pinger
	DataDir string
	Logger  *slog.Logger
	Clock   clock.Clock

	// WorkspaceID is the single workspace, from workspace.Bootstrap.
	WorkspaceID uuid.UUID
	Settings    *workspace.Service
	// LookupAvailable reports whether the optional server-side TMDB key is configured (F013-B5).
	LookupAvailable bool
	// BookLookupAvailable reports whether book lookup (Open Library) is on (F016-B3).
	BookLookupAvailable bool
	// TMDB serves optional movie and TV title suggestions (F013-B1..B3).
	TMDB lookup.Provider
	// Books serves book title suggestions (Open Library, F016-B3).
	Books lookup.Provider
	// Lookup orchestrates item imports and refreshes (F013-B3).
	Lookup *lookup.Service
	// Items serves items (F-019-B3d). The composition root builds it with NewItems and
	// gives the same instance to lookup and images.
	Items *items.Service
	// Search serves GET /search and GET /recent-items (F-019-B3f). The composition root builds it
	// with search.NewService over search.NewPostgres.
	Search *search.Service
	// Images serves item images and attachment files (F-019-B3e). The composition root builds it with
	// images.NewService and gives the same instance to the sweeper.
	Images *images.Service
	// Trash serves the deletes of folders, lists and items, the trash list, restore and purge (F-019-B3f).
	// The composition root builds it with trash.NewService over the pool and the file store.
	Trash *trash.Service
	// Favicons serves GET /favicon. The composition root builds it with favicons.NewService over
	// favicons.NewHTTPFetcher and the file store.
	Favicons *favicons.Service
	// Logs serves the in-memory log buffer and its viewer (LOGS_UI). Nil leaves the routes
	// unregistered, so a disabled instance answers the normal 404 problem.
	Logs *logs.Buffer
	// Identity resolves the caller of every /api/v1 request (F-027). NewRouter refuses to build
	// without it, so a missing wire can never serve unauthenticated data. The composition root
	// builds it with identity.New.
	Identity IdentityResolver
	// Users serves GET /me (the same service the Identity resolver creates users with).
	Users *users.Service
	// ProbeOnly lets NewRouter build without a database pool, hence without the access rules. Only for
	// probe and If-Match tests that serve no data; never set in cmd/api. Without it, a DB that is not
	// a pool is an error, so a router can never serve unfiltered data.
	ProbeOnly bool
	// DevIdentities is non-empty only when DEV_IDENTITY is on; GET /me lists it.
	DevIdentities []string
}

// IdentityResolver is the identity middleware (identity.Resolver).
type IdentityResolver interface {
	Middleware(next http.Handler) http.Handler
}

// NewRouter builds the HTTP handler: the Huma API on chi, behind request id,
// security headers, access log and panic recovery.
func NewRouter(d Deps) (http.Handler, error) {
	if d.Identity == nil {
		return nil, errors.New("httpapi: Deps.Identity is required")
	}
	if !d.ProbeOnly && newAccess(d.DB) == nil {
		return nil, errors.New("httpapi: Deps.DB must be a database pool (access rules); ProbeOnly is for tests that serve no data")
	}
	r, _ := build(d)
	return httpserver.Wrap(r, d.Logger), nil
}

// API returns the Huma API with every operation registered. The OpenAPI spec is
// generated from it (cmd/specgen runs it with a zero Deps), so registration must
// not dereference a dependency: the handlers only use Deps when a request runs.
func API(d Deps) huma.API {
	_, api := build(d)
	return api
}

// build assembles the chi router and the Huma API over it. The Huma operations
// are the whole HTTP surface: unknown paths and wrong methods answer problems.
func build(d Deps) (chi.Router, huma.API) {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	r := chi.NewRouter()
	r.NotFound(httpserver.NotFound)
	r.MethodNotAllowed(httpserver.MethodNotAllowed)
	// The workspace scope is a context value set before any route runs, so it is
	// the same for every operation, the probes included (they never read it).
	r.Use(scope.Middleware(d.WorkspaceID))
	// Identity runs before Huma, so a request without a valid identity never reaches a handler,
	// not even the body or If-Match checks. It only acts on /api/v1 paths.
	if d.Identity != nil {
		r.Use(d.Identity.Middleware)
	}
	r.Use(accessCache)
	api := httpserver.NewAPI(r)
	register(api, d)
	return r, api
}

// register adds every operation of the API: the probes and each feature's
// Register call. Feature services are built here, from the Deps the composition
// root gives.
func register(api huma.API, d Deps) {
	registerHealth(api, d)
	// Visibility (F-027 B2). Without a database pool (the probe-only test routers) nothing is
	// guarded because nothing is served. The guard is registered before the feature operations:
	// Huma reads the middleware list when an operation is added.
	acc := newAccess(d.DB)
	itemSvc, trashSvc, searchSvc := d.Items, d.Trash, d.Search
	if acc != nil {
		api.UseMiddleware(guard(api, acc, d.Logger))
		if itemSvc != nil {
			itemSvc = itemSvc.WithAccess(acc)
		}
		if trashSvc != nil {
			trashSvc = trashSvc.WithAccess(acc)
		}
		if searchSvc != nil {
			searchSvc = searchSvc.WithAccess(acc)
		}
	}
	users.Register(api, d.Logger, d.Users, d.DevIdentities)
	workspace.Register(api, d.Logger, d.Settings, d.Settings, workspace.Availability{
		Lookup:     d.LookupAvailable,
		BookLookup: d.BookLookupAvailable,
	})
	folderRepo := folders.NewPostgres(folderDB(d.DB))
	listRepo := lists.NewPostgres(folderDB(d.DB))
	folderSvc := folders.NewService(folderRepo, d.Clock)
	if d.Users != nil {
		folderSvc = folderSvc.WithUsers(userLookup{d.Users})
	}
	listSvc := lists.NewService(listRepo, listTypes{}, d.Clock)
	var treeAccess tree.Access
	if acc != nil {
		folderSvc, listSvc, treeAccess = folderSvc.WithAccess(acc), listSvc.WithAccess(acc), acc
	}
	folders.Register(api, d.Logger, folderSvc)
	lists.Register(api, d.Logger, listSvc)
	tree.Register(api, d.Logger, folderRepo, listRepo, treeAccess)
	definitions.Register(api, d.Logger)
	items.Register(api, d.Logger, itemSvc, importer(d))
	content.Register(api, d.Logger, content.NewService(content.NewPostgres(contentDB(d.DB)), contentSettings{settingsService(d)}, d.Clock))
	images.Register(api, d.Logger, d.Images)
	trash.Register(api, d.Logger, trashSvc)
	search.Register(api, d.Logger, searchSvc)
	lookup.Register(api, d.Logger, d.Lookup, d.TMDB, d.Books, d.Settings)
	stores.Register(api, d.Logger)
	favicons.Register(api, d.Logger, d.Favicons)
	if d.Logs != nil {
		logs.Register(api, d.Logger, d.Logs)
	}
}

// userLookup is the folders.Users: it finds a user by e-mail without creating one.
type userLookup struct{ svc *users.Service }

func (u userLookup) IDByEmail(ctx context.Context, email string) (uuid.UUID, bool, error) {
	user, ok, err := u.svc.Lookup(ctx, email)
	return user.ID, ok, err
}

// folderDB is the database the folder repository runs on. Deps.DB is the pool in
// production and in the router tests that reach folder routes. The Ping-only
// fakes get a nil database: their folder requests answer before any query (the
// If-Match check comes first), so nothing runs on it.
func folderDB(db Pinger) folders.DB {
	fdb, _ := db.(folders.DB)
	return fdb
}

// listTypes is the lists.TypeSource: it reads a built-in type from the registry and
// converts it to the lists rules' view. The composition root wires it, so lists
// does not import the registry.
type listTypes struct{}

func (listTypes) TypeInfo(_ context.Context, key *string) (lists.TypeInfo, []lists.FieldInfo, error) {
	if key == nil {
		return lists.TypeInfo{}, nil, nil
	}
	def, ok := types.Lookup(*key)
	if !ok {
		return lists.TypeInfo{}, nil, apperr.NotFound("type")
	}
	out := make([]lists.FieldInfo, 0, len(def.Fields))
	for _, f := range def.Fields {
		out = append(out, lists.FieldInfo{Key: f.Key, FieldType: f.Type})
	}
	return lists.TypeInfo{Key: def.Key, AllowsPrice: def.AllowsPrice, DefaultSortField: def.DefaultSortField}, out, nil
}

// NewItems builds the item rules over the pool: a list type is read from the built-in
// registry and the preferred store from the workspace settings. The composition
// root builds one instance and gives it to lookup, images and Deps.Items.
func NewItems(pool *pgxpool.Pool, clk clock.Clock, settings *workspace.Service) *items.Service {
	return items.NewService(items.NewPostgres(pool), itemTypes{}, itemSettings{settings}, clk)
}

// itemTypes is the items.TypeSource: it reads a list type from the built-in registry
// and converts it to the items rules' view, so items does not import the registry's
// callers.
type itemTypes struct{}

func (itemTypes) TypeFields(_ context.Context, key *string) (items.TypeDef, []types.Field, error) {
	if key == nil {
		return items.TypeDef{}, nil, nil
	}
	def, ok := types.Lookup(*key)
	if !ok {
		return items.TypeDef{}, nil, apperr.NotFound("type")
	}
	return items.TypeDef{Key: def.Key, AllowsPrice: def.AllowsPrice}, def.Fields, nil
}

// settingsService is the workspace settings the content rules read: the one the
// composition root wired, or one over the same pool (a router without Settings still
// reads the settings table directly).
func settingsService(d Deps) *workspace.Service {
	if d.Settings != nil {
		return d.Settings
	}
	pool, _ := d.DB.(*pgxpool.Pool)
	return workspace.NewService(workspace.NewPostgres(pool))
}

// itemSettings is the items.SettingsSource over the workspace settings. A workspace
// without a settings row answers NotFound, which the items rules treat as no default.
type itemSettings struct{ svc *workspace.Service }

func (a itemSettings) PreferredStore(ctx context.Context) (string, error) {
	if a.svc == nil {
		return "", apperr.NotFound("settings")
	}
	s, err := a.svc.Get(ctx)
	if err != nil {
		return "", err
	}
	return s.PreferredStore, nil
}

func (a itemSettings) OwnedStreamingServices(ctx context.Context) ([]string, error) {
	if a.svc == nil {
		return nil, apperr.NotFound("settings")
	}
	s, err := a.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return s.OwnedStreamingServices, nil
}

// contentDB is the database the content repository runs on, with the same rule as folderDB.
func contentDB(db Pinger) content.DB {
	cdb, _ := db.(content.DB)
	return cdb
}

// contentSettings is the content.SettingsSource over the workspace settings.
type contentSettings struct{ svc *workspace.Service }

func (a contentSettings) MarkdownHistoryLimit(ctx context.Context) (int, error) {
	s, err := a.svc.Get(ctx)
	if err != nil {
		return 0, err
	}
	return s.MarkdownHistoryLimit, nil
}

// importer is the create path of the items operations when lookup is configured.
func importer(d Deps) items.Importer {
	if d.Lookup == nil {
		return nil
	}
	return importItems{d.Lookup}
}

// importItems is the items.Importer over internal/lookup.
type importItems struct{ lk *lookup.Service }

func (a importItems) Create(ctx context.Context, listID uuid.UUID, in items.CreateInput, o items.ImportOptions) (items.Item, error) {
	return a.lk.Create(ctx, listID, in, lookup.CreateOptions{Lookup: o.Lookup, TMDBID: o.TMDBID, SourceID: o.SourceID})
}
