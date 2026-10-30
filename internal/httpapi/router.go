// Package httpapi holds the HTTP router, middleware, problem+json mapping and
// the handlers behind the generated strict server.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/folders"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/tree"
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
	// Search serves GET /search (B-11).
	Search *search.Service
	// Images serves item images and attachment files (B-09).
	Images *images.Service
	// Trash serves deletes, the trash list, restore and purge (B-10).
	Trash *trash.Service
}

// NewRouter builds the HTTP handler: the generated strict server mounted on
// chi, behind request id, security headers, access log and panic recovery.
func NewRouter(d Deps) (http.Handler, error) {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	return newHandler(newServer(d), d)
}

// newHandler mounts any strict server implementation; tests use it to inject
// handlers that fail or panic.
func newHandler(si oapi.StrictServerInterface, d Deps) (http.Handler, error) {
	doc, err := oapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded openapi spec: %w", err)
	}

	errs := handlerErrors{log: d.Logger}
	strict := oapi.NewStrictHandlerWithOptions(si, nil, oapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  errs.request,
		ResponseErrorHandlerFunc: errs.response,
	})

	r := chi.NewRouter()
	r.NotFound(httpserver.NotFound)
	r.MethodNotAllowed(httpserver.MethodNotAllowed)
	oapi.HandlerWithOptions(strict, oapi.ChiServerOptions{
		BaseRouter:       r,
		Middlewares:      []oapi.MiddlewareFunc{newGuard(doc).middleware},
		ErrorHandlerFunc: errs.parameter,
	})

	// Operations migrated to Huma are registered after the old routes on the same
	// router: chi replaces the old handler of the same method and path, so Huma answers.
	r.Group(func(r chi.Router) {
		r.Use(scope.Middleware(d.WorkspaceID))
		api := httpserver.NewAPI(r)
		workspace.Register(api, d.Logger, d.Settings, d.Settings, workspace.Availability{
			Lookup:     d.LookupAvailable,
			BookLookup: d.BookLookupAvailable,
		})
		folderRepo := folders.NewPostgres(folderDB(d.DB))
		listRepo := lists.NewPostgres(folderDB(d.DB))
		typeDefs := definitions.NewPostgres(typeDB(d.DB))
		folders.Register(api, d.Logger, folders.NewService(folderRepo, d.Clock))
		lists.Register(api, d.Logger, lists.NewService(listRepo, listTypes{typeDefs}, d.Clock))
		tree.Register(api, d.Logger, folderRepo, listRepo)
		definitions.Register(api, d.Logger, definitions.NewService(typeDefs, d.Clock))
		items.Register(api, d.Logger, d.Items, importer(d))
		content.Register(api, d.Logger, content.NewService(content.NewPostgres(contentDB(d.DB)), contentSettings{settingsService(d)}, d.Clock))
	})

	return httpserver.Wrap(r, d.Logger), nil
}

// folderDB is the database the folder repository runs on. Deps.DB is the pool in
// production and in the router tests that reach folder routes. The Ping-only
// fakes get a nil database: their folder requests answer before any query (the
// If-Match check comes first), so nothing runs on it.
func folderDB(db Pinger) folders.DB {
	fdb, _ := db.(folders.DB)
	return fdb
}

// typeDB is the database the type repository runs on, with the same rule as folderDB.
func typeDB(db Pinger) definitions.DB {
	tdb, _ := db.(definitions.DB)
	return tdb
}

// listTypes is the lists.TypeSource: it reads a type from the type definitions and
// converts it to the lists rules' view. The composition root wires it, so lists
// does not import definitions.
type listTypes struct{ defs *definitions.Postgres }

func (a listTypes) TypeInfo(ctx context.Context, key *string) (lists.TypeInfo, []lists.FieldInfo, error) {
	if key == nil {
		return lists.TypeInfo{}, nil, nil
	}
	var typ definitions.Type
	var fields []definitions.Field
	err := a.defs.InTx(ctx, func(r definitions.Repo) error {
		var err error
		if typ, err = r.GetTypeByKey(ctx, *key); err != nil {
			return err
		}
		fields, err = r.ListFields(ctx, typ.ID)
		return err
	})
	if err != nil {
		return lists.TypeInfo{}, nil, err
	}
	out := make([]lists.FieldInfo, 0, len(fields))
	for _, f := range fields {
		out = append(out, lists.FieldInfo{Key: f.Key, FieldType: f.Type})
	}
	return lists.TypeInfo{Key: typ.Key, AllowsPrice: typ.AllowsPrice, DefaultSortField: typ.DefaultSortField}, out, nil
}

// NewItems builds the item rules over the pool: a list type is read from the type
// definitions and the preferred store from the workspace settings. The composition
// root builds one instance and gives it to lookup, images and Deps.Items.
func NewItems(pool *pgxpool.Pool, clk clock.Clock, settings *workspace.Service) *items.Service {
	return items.NewService(items.NewPostgres(pool), itemTypes{definitions.NewPostgres(pool)}, itemSettings{settings}, clk)
}

// itemTypes is the items.TypeSource: it reads a list type from the type definitions
// and converts it to the items rules' view, so items does not import definitions.
type itemTypes struct{ defs *definitions.Postgres }

func (a itemTypes) TypeFields(ctx context.Context, key *string) (items.TypeDef, []types.Field, error) {
	if key == nil {
		return items.TypeDef{}, nil, nil
	}
	var typ definitions.Type
	var fields []definitions.Field
	err := a.defs.InTx(ctx, func(r definitions.Repo) error {
		var err error
		if typ, err = r.GetTypeByKey(ctx, *key); err != nil {
			return err
		}
		fields, err = r.ListFields(ctx, typ.ID)
		return err
	})
	if err != nil {
		return items.TypeDef{}, nil, err
	}
	out := make([]types.Field, 0, len(fields))
	for _, f := range fields {
		out = append(out, types.Field{Key: f.Key, Type: f.Type, Special: f.Locked})
	}
	return items.TypeDef{Key: typ.Key, AllowsPrice: typ.AllowsPrice}, out, nil
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
