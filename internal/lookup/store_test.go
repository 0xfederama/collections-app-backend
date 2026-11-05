package lookup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

var lookupAdminURL string

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

type lookupEnv struct {
	t        *testing.T
	db       DB
	files    storage.FileStore
	clock    *fakeClock
	items    *items.Service
	store    *DBStore
	ws, list uuid.UUID
}

func newLookupEnv(t *testing.T, now time.Time) *lookupEnv {
	t.Helper()
	if lookupAdminURL == "" {
		lookupAdminURL = testdb.AdminURL(t)
	}
	db := testdb.NewMigrated(t, lookupAdminURL)
	ws, list := uuid.New(), uuid.New()
	if _, err := db.Exec(t.Context(), `INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO workspace_settings (workspace_id) VALUES ($1)`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Movies', 'movie', 0)`, list, ws); err != nil {
		t.Fatal(err)
	}
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	clk := &fakeClock{now: now}
	itemSvc := items.NewService(items.NewPostgres(db), envTypes{}, envSettings{workspace.NewService(workspace.NewPostgres(db))}, clk)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &lookupEnv{t: t, db: db, files: local, clock: clk, items: itemSvc, store: NewStore(db, local, clk, itemSvc, logger, ws), ws: ws, list: list}
}

// envTypes reads a list type from the built-in registry, the way the composition
// root wires the items type source (httpapi/router.go itemTypes).
type envTypes struct{}

func (envTypes) TypeFields(_ context.Context, key *string) (items.TypeDef, []types.Field, error) {
	if key == nil {
		return items.TypeDef{}, nil, nil
	}
	def, ok := types.Lookup(*key)
	if !ok {
		return items.TypeDef{}, nil, apperr.NotFound("type")
	}
	return items.TypeDef{Key: def.Key, AllowsPrice: def.AllowsPrice}, def.Fields, nil
}

// envSettings reads the preferred store from the workspace settings (httpapi itemSettings).
type envSettings struct{ svc *workspace.Service }

func (a envSettings) PreferredStore(ctx context.Context) (string, error) {
	s, err := a.svc.Get(ctx)
	if err != nil {
		return "", err
	}
	return s.PreferredStore, nil
}

func (a envSettings) OwnedStreamingServices(ctx context.Context) ([]string, error) {
	s, err := a.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return s.OwnedStreamingServices, nil
}

func (e *lookupEnv) create(title string, metadata map[string]any) items.Item {
	e.t.Helper()
	it, err := e.items.Create(scope.With(e.t.Context(), e.ws), e.list, items.CreateInput{Title: title, Metadata: metadata})
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func processedPoster(seed string) *images.Processed {
	original := []byte("poster-" + seed)
	return &images.Processed{Original: original, Thumbnail: []byte("thumb-" + seed), MediaType: "image/jpeg", Ext: "jpg", Width: 1, Height: 1, SHA256: sha256.Sum256(original)}
}

func decodeMetadata(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestF013B3_ApplyCreateTracksOnlyImportedLeaves(t *testing.T) {
	now := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, now)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix",
		CreateInput: &items.CreateInput{Title: "matrix", Metadata: map[string]any{"director": "My director"}},
		Fields: map[string]any{
			"release_year": 1999, "director": "The Wachowskis",
			"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix"}},
		},
		PosterPath: "/matrix.jpg", Poster: processedPoster("create"), RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.Version != 1 || it.Title != "The Matrix" || it.Image == nil {
		t.Fatalf("created item = title %q version %d image %v", it.Title, it.Version, it.Image)
	}
	metadata := decodeMetadata(t, it.Metadata)
	if metadata["director"] != "My director" || metadata["tmdb_id"] != float64(603) {
		t.Fatalf("metadata = %#v", metadata)
	}
	record, ok, err := e.store.Read(t.Context(), it.ID)
	if err != nil || !ok {
		t.Fatalf("record ok=%v err=%v", ok, err)
	}
	if _, claimed := record.ImportedFields["director"]; claimed {
		t.Error("explicit director was claimed as imported")
	}
	want := map[string]any{"release_year": float64(1999), "streaming.platforms": []any{"Netflix"}, "tmdb_id": float64(603)}
	if !reflect.DeepEqual(record.ImportedFields, want) {
		t.Errorf("snapshots = %#v, want %#v", record.ImportedFields, want)
	}
	if record.AttachmentID == nil || record.PosterFetchedAt == nil || !record.FetchedAt.Equal(now) {
		t.Errorf("poster provenance = %#v", record)
	}
	var refreshAfter, expiresAt, posterExpires time.Time
	if err := e.db.QueryRow(t.Context(), `SELECT refresh_after, expires_at, poster_expires_at FROM lookup_imports WHERE item_id = $1`, it.ID).Scan(&refreshAfter, &expiresAt, &posterExpires); err != nil {
		t.Fatal(err)
	}
	if wantRefresh := time.Date(2026, time.June, 30, 12, 0, 0, 0, time.UTC); !refreshAfter.Equal(wantRefresh) {
		t.Errorf("refresh_after = %s, want %s", refreshAfter, wantRefresh)
	}
	if wantExpiry := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC); !expiresAt.Equal(wantExpiry) || !posterExpires.Equal(wantExpiry) {
		t.Errorf("expiries = %s/%s, want %s", expiresAt, posterExpires, wantExpiry)
	}
}

func TestF013B3_StaleApplyLeavesNoRowsOrFiles(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	it := e.create("Matrix", nil)
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version + 1, Type: "movie",
		Provider: ProviderTMDB, SourceID: "603", Title: "The Matrix", OriginalTitle: it.Title,
		Fields: map[string]any{"release_year": 1999}, Poster: processedPoster("stale"), PosterPath: "/stale.jpg",
	})
	var conflict *apperr.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}
	var imports, attachments int
	if err := e.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM lookup_imports), (SELECT count(*) FROM attachments)`).Scan(&imports, &attachments); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || attachments != 0 {
		t.Fatalf("imports=%d attachments=%d, want zero", imports, attachments)
	}
	files := 0
	if err := e.files.Walk("images", func(string, time.Time) error { files++; return nil }); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Errorf("orphan files = %d, want zero", files)
	}
}
