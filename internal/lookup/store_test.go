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
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/seed"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/testdb"
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
	if err := seed.BuiltinTypes(t.Context(), sqlc.New(db), ws); err != nil {
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
	itemSvc := items.New(db, clk, ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &lookupEnv{t: t, db: db, files: local, clock: clk, items: itemSvc, store: NewStore(db, local, clk, itemSvc, logger, ws), ws: ws, list: list}
}

func (e *lookupEnv) create(title string, metadata map[string]any) items.Item {
	e.t.Helper()
	it, err := e.items.Create(e.t.Context(), e.list, items.CreateInput{Title: title, Metadata: metadata})
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

func TestF013B3_OwnedRefreshPreservesUserEditedLeaf(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, now)
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: it.Title, Fields: map[string]any{"release_year": 1999, "runtime_minutes": 136},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET metadata = jsonb_set(metadata, '{runtime_minutes}', '140'), version = version + 1 WHERE id = $1`, it.ID); err != nil {
		t.Fatal(err)
	}
	it, err = e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix",
		Fields:        map[string]any{"release_year": 2000, "runtime_minutes": 137, "director": "The Wachowskis"},
		RefreshFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.items.Get(t.Context(), it.ID)
	metadata := decodeMetadata(t, got.Metadata)
	if metadata["release_year"] != float64(2000) || metadata["runtime_minutes"] != float64(140) || metadata["director"] != "The Wachowskis" {
		t.Errorf("metadata after owned refresh = %#v", metadata)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if _, reclaimed := record.ImportedFields["runtime_minutes"]; reclaimed {
		t.Error("user-edited runtime was reclaimed")
	}
}

func TestF013B3_PosterOnlyRefreshDoesNotRenewMetadata(t *testing.T) {
	start := time.Date(2026, time.January, 31, 9, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: it.Title, Fields: map[string]any{"release_year": 1999},
		Poster: processedPoster("old"), PosterPath: "/old.jpg", RefreshFields: true, RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, time.June, 30, 9, 0, 0, 0, time.UTC))
	_, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "Changed upstream", OriginalTitle: "Matrix", Fields: map[string]any{"release_year": 2000},
		Poster: processedPoster("fresh"), PosterPath: "/fresh.jpg", RefreshFields: false, RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := e.store.Read(t.Context(), it.ID)
	if err != nil || !ok {
		t.Fatalf("record ok=%v err=%v", ok, err)
	}
	if !record.FetchedAt.Equal(start) || record.PosterFetchedAt == nil || !record.PosterFetchedAt.Equal(e.clock.Now()) {
		t.Errorf("field fetched=%s poster fetched=%v", record.FetchedAt, record.PosterFetchedAt)
	}
	got, err := e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Matrix" || decodeMetadata(t, got.Metadata)["release_year"] != float64(1999) {
		t.Errorf("poster-only refresh changed fields: title=%q metadata=%s", got.Title, got.Metadata)
	}
}

func TestF013B3_MaintenanceFailureBeforeExpiryThenPurgesAtBoundary(t *testing.T) {
	start := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: it.Title, Fields: map[string]any{"release_year": 1999},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, time.June, 30, 12, 0, 0, 0, time.UTC))
	calls := 0
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		calls++
		return apperr.LookupDisabled()
	})
	if err != nil || result.Failed != 1 || calls != 1 {
		t.Fatalf("before expiry result=%+v calls=%d err=%v", result, calls, err)
	}
	if _, ok, _ := e.store.Read(t.Context(), it.ID); !ok {
		t.Fatal("provenance removed before six-month boundary")
	}
	e.clock.Set(time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC))
	result, err = e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		calls++
		return nil
	})
	if err != nil || result.Expired != 1 {
		t.Fatalf("at expiry result=%+v err=%v", result, err)
	}
	if calls != 1 {
		t.Errorf("expired item was refreshed, calls=%d", calls)
	}
	got, err := e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "matrix" || len(decodeMetadata(t, got.Metadata)) != 0 {
		t.Errorf("expired item title=%q metadata=%s", got.Title, got.Metadata)
	}
	if _, ok, _ := e.store.Read(t.Context(), it.ID); ok {
		t.Error("expired provenance still exists")
	}
}

func TestF013B3_ExpiryPreservesManualPhotoAndCoversTrashedItems(t *testing.T) {
	start := time.Date(2026, time.February, 28, 8, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: it.Title, Fields: map[string]any{"release_year": 1999},
		Poster: processedPoster("owned"), PosterPath: "/owned.jpg", RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	manualID := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO attachments
		(id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, $2, 'images/manual.jpg', 'images/manual.thumb.jpg', 'image/jpeg', 1, 1, 1, '\x00')`, manualID, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET image_attachment_id = $2, version = version + 1 WHERE id = $1`, it.ID, manualID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `DELETE FROM attachments WHERE id = $1`, *record.AttachmentID); err != nil {
		t.Fatal(err)
	}
	batchID := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
		VALUES ($1, $2, 'item', $3, 'The Matrix', '[]', $4)`, batchID, e.ws, it.ID, start); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET deleted_at = $2, trash_batch_id = $3 WHERE id = $1`, it.ID, start, batchID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC))
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		t.Fatal("trashed expired item must not refresh")
		return nil
	})
	if err != nil || result.Expired != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var title string
	var metadata []byte
	var attachment uuid.UUID
	if err := e.db.QueryRow(t.Context(), `SELECT title, metadata, image_attachment_id FROM items WHERE id = $1`, it.ID).Scan(&title, &metadata, &attachment); err != nil {
		t.Fatal(err)
	}
	if title != "matrix" || string(metadata) != "{}" || attachment != manualID {
		t.Errorf("trashed expiry title=%q metadata=%s attachment=%s", title, metadata, attachment)
	}
}

func TestF013B3_MetadataExpiryPreservesYoungerPoster(t *testing.T) {
	start := time.Date(2026, time.January, 31, 8, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: it.Title, Fields: map[string]any{"release_year": 1999},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, time.February, 28, 8, 0, 0, 0, time.UTC))
	it, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", Fields: map[string]any{"release_year": 2000},
		Poster: processedPoster("younger"), PosterPath: "/younger.jpg", RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, time.July, 31, 8, 0, 0, 0, time.UTC))
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		t.Fatal("expired metadata must not trigger a refresh before poster expiry")
		return nil
	})
	if err != nil || result.Expired != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got, err := e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "matrix" || len(decodeMetadata(t, got.Metadata)) != 0 || got.Image == nil {
		t.Errorf("metadata expiry title=%q metadata=%s image=%v", got.Title, got.Metadata, got.Image)
	}
	record, ok, err := e.store.Read(t.Context(), it.ID)
	if err != nil || !ok || record.AttachmentID == nil || record.PosterFetchedAt == nil {
		t.Fatalf("poster provenance after metadata expiry = %#v, ok=%v err=%v", record, ok, err)
	}
}
