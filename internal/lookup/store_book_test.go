package lookup

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

// F016-B3: book (Open Library) provenance. Open Library imports have no retention
// (D-095) but keep provenance so fill, rematch and refresh know what was imported.

func (e *lookupEnv) bookList() uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	if _, err := e.db.Exec(e.t.Context(), `INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Books', 'book', 1)`, id, e.ws); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *lookupEnv) createBook(list uuid.UUID, title string, metadata map[string]any) items.Item {
	e.t.Helper()
	it, err := e.items.Create(e.t.Context(), list, items.CreateInput{Title: title, Metadata: metadata})
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func bookFields() map[string]any {
	return map[string]any{"author": "Frank Herbert", "pages": 608, "isbn": "9780441172719"}
}

func TestF016B3_BookCreateKeepsProvenanceWithoutRetention(t *testing.T) {
	now := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, now)
	list := e.bookList()
	amount := "12.00"
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: list, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL893415W",
		Title: "Dune", OriginalTitle: "dune",
		CreateInput: &items.CreateInput{Title: "dune", Price: &items.Price{Amount: &amount, Currency: "EUR"}, Metadata: map[string]any{"pages": 700}},
		Fields:      bookFields(), FillOnly: map[string]any{"stores": []string{"amazon", "kobo"}},
		PosterPath: "12547191", Poster: processedPoster("cover"), RefreshFields: true, RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.Title != "dune" || it.Image == nil || it.Price == nil || it.Price.Currency != "EUR" || it.Type == nil || *it.Type != "book" {
		t.Fatalf("created item = %+v", it)
	}
	metadata := decodeMetadata(t, it.Metadata)
	want := map[string]any{
		"author": "Frank Herbert", "pages": float64(700), "isbn": "9780441172719",
		"openlibrary_id": "OL893415W", "stores": []any{"amazon", "kobo"},
	}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("metadata = %#v, want %#v", metadata, want)
	}
	record, ok, err := e.store.Read(t.Context(), it.ID)
	if err != nil || !ok || record.Provider != ProviderOpenLibrary || record.SourceID != "OL893415W" || record.Type != "book" {
		t.Fatalf("record = %+v ok=%v err=%v", record, ok, err)
	}
	// The user's pages and the fill-only stores are not claimed as imported.
	wantImported := map[string]any{"author": "Frank Herbert", "isbn": "9780441172719", "openlibrary_id": "OL893415W"}
	if !reflect.DeepEqual(record.ImportedFields, wantImported) {
		t.Errorf("snapshots = %#v, want %#v", record.ImportedFields, wantImported)
	}
	if record.AttachmentID == nil || record.PosterFetchedAt == nil || record.PosterPath != "12547191" {
		t.Errorf("cover provenance = %+v", record)
	}
	var deadlines int
	if err := e.db.QueryRow(t.Context(), `SELECT count(*) FROM lookup_imports WHERE item_id = $1 AND provider = 'openlibrary'
		AND refresh_after IS NULL AND expires_at IS NULL AND poster_refresh_after IS NULL AND poster_expires_at IS NULL`, it.ID).Scan(&deadlines); err != nil || deadlines != 1 {
		t.Errorf("open library row without deadlines: count = %d err = %v", deadlines, err)
	}
}

func TestF016B3_BookCreateExplicitStoresWinOverTheProviderLink(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	list := e.bookList()
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: list, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
		Title: "Dune", OriginalTitle: "dune", CreateInput: &items.CreateInput{Title: "dune", Metadata: map[string]any{"stores": []any{}}},
		Fields: bookFields(), FillOnly: map[string]any{"stores": []string{"amazon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stores := decodeMetadata(t, it.Metadata)["stores"]; !reflect.DeepEqual(stores, []any{}) {
		t.Errorf("explicit empty stores = %#v, want []", stores)
	}
}

func TestF016B3_BookFillSetsOnlyEmptyValuesAndTheStoreLinkOnlyWhenEmpty(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	list := e.bookList()
	it := e.createBook(list, "dune", map[string]any{"author": "Mine", "stores": []any{}})
	apply := func(it items.Item) items.Item {
		t.Helper()
		got, err := e.store.Apply(t.Context(), ApplyParams{
			Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
			Title: "Dune", OriginalTitle: it.Title, Fields: bookFields(), FillOnly: map[string]any{"stores": []string{"amazon"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	it = apply(it)
	metadata := decodeMetadata(t, it.Metadata)
	if metadata["author"] != "Mine" || metadata["pages"] != float64(608) || metadata["isbn"] != "9780441172719" ||
		metadata["openlibrary_id"] != "OL1W" || !reflect.DeepEqual(metadata["stores"], []any{"amazon"}) {
		t.Fatalf("metadata after fill = %#v", metadata)
	}
	if it.Title != "dune" {
		t.Errorf("fill replaced a user title: %q", it.Title)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if _, claimed := record.ImportedFields["author"]; claimed {
		t.Error("the user's author was claimed as imported")
	}
	if _, claimed := record.ImportedFields["stores"]; claimed {
		t.Error("the fill-only stores were claimed as imported")
	}

	// A user choice is never touched: replace the stores, fill again.
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET metadata = jsonb_set(metadata, '{stores}', '["kobo"]'), version = version + 1 WHERE id = $1`, it.ID); err != nil {
		t.Fatal(err)
	}
	it, err := e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	it = apply(it)
	if stores := decodeMetadata(t, it.Metadata)["stores"]; !reflect.DeepEqual(stores, []any{"kobo"}) {
		t.Errorf("stores after a second fill = %#v, want the user's [kobo]", stores)
	}
}

func TestF016B3_BookRematchReplacesFetchedFieldsAndKeepsStoreChoices(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	list := e.bookList()
	it := e.createBook(list, "dune", map[string]any{"stores": []any{"kobo"}})
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
		Title: "Dune", OriginalTitle: it.Title, Fields: bookFields(), FillOnly: map[string]any{"stores": []string{"amazon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The user edits the ISBN by hand.
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET metadata = jsonb_set(metadata, '{isbn}', '"9781111111111"'), version = version + 1 WHERE id = $1`, it.ID); err != nil {
		t.Fatal(err)
	}
	it, _ = e.items.Get(t.Context(), it.ID)
	it, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeRematch, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL2W",
		Title: "Another Dune", OriginalTitle: it.Title,
		Fields:   map[string]any{"author": "Someone Else"}, // no pages, no isbn
		FillOnly: map[string]any{"stores": []string{"amazon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeMetadata(t, it.Metadata)
	if metadata["author"] != "Someone Else" || metadata["openlibrary_id"] != "OL2W" {
		t.Errorf("metadata after rematch = %#v", metadata)
	}
	if _, stale := metadata["pages"]; stale {
		t.Error("an unchanged imported value that the new match lacks was kept")
	}
	if metadata["isbn"] != "9781111111111" {
		t.Errorf("the user's edited isbn = %v, want it kept", metadata["isbn"])
	}
	if !reflect.DeepEqual(metadata["stores"], []any{"kobo"}) {
		t.Errorf("stores = %#v, rematch must never touch the user's choices", metadata["stores"])
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if record.SourceID != "OL2W" || record.Provider != ProviderOpenLibrary {
		t.Errorf("record = %+v", record)
	}
}

func TestF016B3_BookCoverIsOnlyAttachedToAnItemWithoutImage(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	list := e.bookList()
	it := e.createBook(list, "dune", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
		Title: "Dune", OriginalTitle: it.Title, Fields: bookFields(), PosterPath: "1", Poster: processedPoster("one"), RefreshPoster: true,
	})
	if err != nil || it.Image == nil {
		t.Fatalf("first cover: image=%v err=%v", it.Image, err)
	}
	first := it.Image.AttachmentID
	got, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeRefresh, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
		Title: "Dune", OriginalTitle: it.Title, Fields: bookFields(), PosterPath: "2", Poster: processedPoster("two"), RefreshPoster: true,
	})
	if err != nil || got.Image == nil || got.Image.AttachmentID != first {
		t.Fatalf("an existing image was replaced by refresh: image=%+v first=%v err=%v", got.Image, first, err)
	}
}

func TestF016B3_MaintenanceNeverTouchesBookImports(t *testing.T) {
	start := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	list := e.bookList()
	it := e.createBook(list, "dune", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "book", Provider: ProviderOpenLibrary, SourceID: "OL1W",
		Title: "Dune", OriginalTitle: it.Title, Fields: bookFields(), PosterPath: "1", Poster: processedPoster("keep"), RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(start.AddDate(10, 0, 0))
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		t.Error("maintenance refreshed an Open Library import")
		return nil
	})
	if err != nil || result != (MaintenanceResult{}) {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	got, err := e.items.Get(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "dune" || got.Image == nil || decodeMetadata(t, got.Metadata)["author"] != "Frank Herbert" {
		t.Errorf("a book import was expired: title=%q image=%v metadata=%s", got.Title, got.Image, got.Metadata)
	}
	if _, ok, _ := e.store.Read(t.Context(), it.ID); !ok {
		t.Error("the book provenance was removed")
	}
}

func TestF016B3_ApplyRejectsProviderTypeMismatchesAndBadIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ApplyParams
		ok   bool
		want string
	}{
		{"book on openlibrary", ApplyParams{Provider: ProviderOpenLibrary, Type: "book", SourceID: "OL1W", Title: "x", Mode: ModeFill}, true, ""},
		{"movie on tmdb", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "1", Title: "x", Mode: ModeFill}, true, ""},
		{"book on tmdb", ApplyParams{Provider: ProviderTMDB, Type: "book", SourceID: "1", Title: "x", Mode: ModeFill}, false, "type"},
		{"movie on openlibrary", ApplyParams{Provider: ProviderOpenLibrary, Type: "movie", SourceID: "OL1W", Title: "x", Mode: ModeFill}, false, "type"},
		{"unknown provider", ApplyParams{Provider: "google", Type: "book", SourceID: "1", Title: "x", Mode: ModeFill}, false, "type"},
		{"bad work id", ApplyParams{Provider: ProviderOpenLibrary, Type: "book", SourceID: "OL1", Title: "x", Mode: ModeFill}, false, "source_id"},
		{"bad tmdb id", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "0", Title: "x", Mode: ModeFill}, false, "tmdb_id"},
		{"too large tmdb id", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "2147483648", Title: "x", Mode: ModeFill}, false, "tmdb_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApply(tc.p)
			if tc.ok {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var validation *apperr.ValidationError
			if !errors.As(err, &validation) || validation.Errors[0].Field != tc.want {
				t.Fatalf("err = %v, want a validation error on %q", err, tc.want)
			}
		})
	}
}

func TestFillOnlyHelper(t *testing.T) {
	for _, tc := range []struct {
		name       string
		metadata   map[string]any
		emptyLists bool
		want       any
	}{
		{"absent", map[string]any{}, false, []any{"amazon"}},
		{"null", map[string]any{"stores": nil}, false, []any{"amazon"}},
		{"empty list kept on create", map[string]any{"stores": []any{}}, false, []any{}},
		{"empty list filled on fill", map[string]any{"stores": []any{}}, true, []any{"amazon"}},
		{"user choice kept", map[string]any{"stores": []any{"kobo"}}, true, []any{"kobo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fillOnly(tc.metadata, map[string]any{"stores": []string{"amazon"}}, tc.emptyLists)
			if !reflect.DeepEqual(tc.metadata["stores"], tc.want) {
				t.Errorf("stores = %#v, want %#v", tc.metadata["stores"], tc.want)
			}
		})
	}
}

func TestHardcover_UntrackedCreateKeepsTypedTitleAndWritesNoProvenance(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	list := e.bookList()
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreateUntracked, ListID: list, Type: "book", Title: "dune", OriginalTitle: "dune",
		CreateInput: &items.CreateInput{Title: "dune", Metadata: map[string]any{"pages": 700}},
		Fields:      bookFields(), FillOnly: map[string]any{"stores": []string{"kobo"}},
		Poster: processedPoster("hc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.Title != "dune" || it.Image == nil {
		t.Fatalf("item = %+v", it)
	}
	want := map[string]any{"author": "Frank Herbert", "pages": float64(700), "isbn": "9780441172719", "stores": []any{"kobo"}}
	if got := decodeMetadata(t, it.Metadata); !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	var n int
	if err := e.db.QueryRow(t.Context(), `SELECT count(*) FROM lookup_imports WHERE item_id = $1`, it.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("lookup_imports rows = %d err = %v, want none", n, err)
	}
}

func TestHardcover_UntrackedCreateRejectsNonBooks(t *testing.T) {
	e := newLookupEnv(t, time.Now().UTC())
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreateUntracked, ListID: e.bookList(), Type: "movie", Title: "x", CreateInput: &items.CreateInput{Title: "x"},
	})
	if err == nil {
		t.Fatal("untracked create of a movie must fail")
	}
}
