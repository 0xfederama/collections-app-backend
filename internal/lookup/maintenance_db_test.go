package lookup

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// These tests run the weekly pass (D-123) against Postgres, so the SQL selection
// and the scheduled apply rules are checked for real.

// fetchTMDB imports a movie or TV item as a fill at the given time, then restores the clock.
func fetchTMDB(t *testing.T, e *lookupEnv, it items.Item, typ string, at time.Time, fields map[string]any, poster string) items.Item {
	t.Helper()
	saved := e.clock.Now()
	e.clock.Set(at)
	defer e.clock.Set(saved)
	p := ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: typ, Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", Fields: fields,
	}
	if poster != "" {
		p.PosterPath, p.Poster, p.RefreshPoster = "/"+poster+".jpg", processedPoster(poster), true
	}
	got, err := e.store.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestWeekly_ScheduledRefreshOverwritesEditsButKeepsUserImage(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it = fetchTMDB(t, e, it, "movie", start, map[string]any{"release_year": 1999, "runtime_minutes": 136}, "")

	// The user edits a fetched field and an own field, then replaces the image.
	manual := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO attachments
		(id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, $2, 'images/manual.jpg', 'images/manual.thumb.jpg', 'image/jpeg', 1, 1, 1, '\x00')`, manual, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET title = 'My Matrix',
		metadata = jsonb_set(metadata, '{runtime_minutes}', '140') || '{"director":"Mine"}'::jsonb,
		image_attachment_id = $2, version = version + 1 WHERE id = $1`, it.ID, manual); err != nil {
		t.Fatal(err)
	}
	it, err := e.items.Get(scope.With(t.Context(), e.ws), it.ID)
	if err != nil {
		t.Fatal(err)
	}

	e.clock.Set(start.AddDate(0, 0, 8))
	got, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeScheduled, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "My Matrix", Fields: map[string]any{"release_year": 2000, "runtime_minutes": 137},
		PosterPath: "/new.jpg", Poster: processedPoster("new"), RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "The Matrix" {
		t.Errorf("title = %q, want the fetched title", got.Title)
	}
	metadata := decodeMetadata(t, got.Metadata)
	if metadata["release_year"] != float64(2000) || metadata["runtime_minutes"] != float64(137) {
		t.Errorf("fetched fields = %#v, want the new values over the user's edit", metadata)
	}
	if metadata["director"] != "Mine" {
		t.Errorf("own field director = %v, want kept", metadata["director"])
	}
	if got.Image == nil || got.Image.AttachmentID != manual {
		t.Errorf("image = %+v, want the user's image kept", got.Image)
	}
	record, ok, err := e.store.Read(t.Context(), it.ID)
	if err != nil || !ok || !record.FetchedAt.Equal(e.clock.Now()) {
		t.Fatalf("provenance after the weekly refresh = %+v ok=%v err=%v", record, ok, err)
	}
	if _, claimed := record.ImportedFields["director"]; claimed {
		t.Error("the user's own field was claimed as imported")
	}
}

func TestWeekly_ScheduledRefreshRemovesFieldsTMDBNoLongerReturns(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it = fetchTMDB(t, e, it, "movie", start, map[string]any{"release_year": 1999, "runtime_minutes": 136}, "")
	got, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeScheduled, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", Fields: map[string]any{"release_year": 1999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata := decodeMetadata(t, got.Metadata); len(metadata) != 2 || metadata["release_year"] != float64(1999) || metadata["tmdb_id"] != float64(603) {
		t.Errorf("metadata = %#v, want the runtime TMDB no longer returns gone", metadata)
	}
}

func TestWeekly_ScheduledPosterFollowsOwnedPoster(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("matrix", nil)
	it = fetchTMDB(t, e, it, "movie", start, map[string]any{"release_year": 1999}, "old")
	first := it.Image.AttachmentID

	got, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeScheduled, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", Fields: map[string]any{"release_year": 1999},
		PosterPath: "/new.jpg", Poster: processedPoster("new"), RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Image == nil || got.Image.AttachmentID == first {
		t.Fatalf("an owned poster whose TMDB path changed was not replaced: image=%+v", got.Image)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if record.PosterPath != "/new.jpg" {
		t.Errorf("poster path = %q, want the new path", record.PosterPath)
	}
}

func TestWeekly_ListScheduledItemsFollowsTheRules(t *testing.T) {
	start := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	ctx := scope.With(t.Context(), e.ws)
	series := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Series', 'tv_show', 1)`, series, e.ws); err != nil {
		t.Fatal(err)
	}
	add := func(list uuid.UUID, title string, metadata map[string]any) items.Item {
		t.Helper()
		it, err := e.items.Create(ctx, list, items.CreateInput{Title: title, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	// Eligible: a typed tmdb_id without provenance (first), and two fetched long enough ago.
	typed := add(e.list, "typed", map[string]any{"tmdb_id": 700})
	due := fetchTMDB(t, e, add(e.list, "due", nil), "movie", start.AddDate(0, 0, -8), map[string]any{"release_year": 1999}, "")
	show := fetchTMDB(t, e, add(series, "show", map[string]any{"tmdb_id": 705}), "tv_show", start.AddDate(0, 0, -9), map[string]any{"first_air_year": 2000}, "")
	// Excluded: fetched a week ago or less, trashed, lookup_disabled, no TMDB id, a book.
	fetchTMDB(t, e, add(e.list, "not due", nil), "movie", start.AddDate(0, 0, -6), map[string]any{"release_year": 1999}, "")
	trashed := fetchTMDB(t, e, add(e.list, "trashed", nil), "movie", start.AddDate(0, 0, -8), map[string]any{"release_year": 1999}, "")
	trashItem(t, e, trashed.ID, start)
	fetchTMDB(t, e, add(e.list, "lookup off", map[string]any{"lookup_disabled": true}), "movie", start.AddDate(0, 0, -8), map[string]any{"release_year": 1999}, "")
	add(e.list, "no id", map[string]any{"director": "Someone"})
	book := add(e.bookList(), "book", nil)
	openLibraryImport(t, e, book.ID)

	var calls []uuid.UUID
	result, err := e.store.Maintain(t.Context(), func(_ context.Context, id uuid.UUID, _ int) error {
		calls = append(calls, id)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Never fetched sorts first; then the oldest fetch (the series, 9 days ago, before the movie, 8 days ago).
	want := []uuid.UUID{typed.ID, show.ID, due.ID}
	if len(calls) != len(want) {
		t.Fatalf("refreshed %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("refreshed %v, want %v", calls, want)
		}
	}
	if result != (MaintenanceResult{Refreshed: 3}) {
		t.Errorf("result = %+v, want three refreshes", result)
	}
}

// trashItem moves an item to the trash the way the trash service does: a batch row
// and the deleted_at and trash_batch_id pair that items_trash_check requires.
func trashItem(t *testing.T, e *lookupEnv, id uuid.UUID, at time.Time) {
	t.Helper()
	batch := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
		VALUES ($1, $2, 'item', $3, 'trashed', '[]', $4)`, batch, e.ws, id, at); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET deleted_at = $2, trash_batch_id = $3 WHERE id = $1`, id, at, batch); err != nil {
		t.Fatal(err)
	}
}

// openLibraryImport gives a book Open Library provenance, which the weekly pass
// must never list (D-095, D-123).
func openLibraryImport(t *testing.T, e *lookupEnv, id uuid.UUID) {
	t.Helper()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO lookup_imports
		(workspace_id, item_id, provider, source_id, type, original_title, fetched_at)
		VALUES ($1, $2, 'openlibrary', 'OL1W', 'book', 'book', $3)`, e.ws, id, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}
