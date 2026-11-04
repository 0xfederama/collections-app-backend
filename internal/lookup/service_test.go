package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// fakeClient is a fake Provider. name defaults to the TMDB provider.
type fakeClient struct {
	name           string
	enabled        bool
	matches        []Suggestion
	details        Details
	err            error
	downloads      int
	searchType     string
	searchQuery    string
	searchLanguage string
	detailsID      string
	downloadErr    error
	// image is the poster DownloadImage returns when set (a valid PNG for Process).
	image []byte
}

func (f *fakeClient) Name() string {
	if f.name == "" {
		return ProviderTMDB
	}
	return f.name
}
func (f *fakeClient) Enabled() bool { return f.enabled }
func (f *fakeClient) ValidateSourceID(id string) error {
	return ValidSourceID(f.Name(), id)
}
func (f *fakeClient) Search(_ context.Context, typ, query, language string) ([]Suggestion, error) {
	f.searchType, f.searchQuery, f.searchLanguage = typ, query, language
	return f.matches, f.err
}
func (f *fakeClient) Details(_ context.Context, _, id, _, _ string) (Details, error) {
	f.detailsID = id
	return f.details, f.err
}
func (f *fakeClient) DownloadImage(context.Context, string) ([]byte, error) {
	f.downloads++
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if f.image != nil {
		return f.image, f.err
	}
	return nil, f.err
}

type fakeStore struct {
	typ     string
	record  Record
	found   bool
	applied *ApplyParams
	result  items.Item
	err     error
}

func (f *fakeStore) ListType(context.Context, uuid.UUID) (string, error) { return f.typ, f.err }
func (f *fakeStore) Read(context.Context, uuid.UUID) (Record, bool, error) {
	return f.record, f.found, f.err
}
func (f *fakeStore) Apply(_ context.Context, p ApplyParams) (items.Item, error) {
	f.applied = &p
	return f.result, f.err
}

type fakeItems struct {
	item        items.Item
	created     *items.CreateInput
	updateCalls int
	err         error
}

func (f *fakeItems) Get(context.Context, uuid.UUID) (items.Item, error) { return f.item, f.err }
func (f *fakeItems) Create(_ context.Context, _ uuid.UUID, in items.CreateInput) (items.Item, error) {
	f.created = &in
	return f.item, f.err
}
func (f *fakeItems) Update(_ context.Context, _ uuid.UUID, _ int, _ items.Patch) (items.Item, error) {
	f.updateCalls++
	return f.item, f.err
}

type fakeSettings struct{ value workspace.Settings }

func (f fakeSettings) Get(context.Context) (workspace.Settings, error) {
	return f.value, nil
}

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

func newService(client *fakeClient, store *fakeStore, itemSvc *fakeItems) *Service {
	return newServiceWith(Providers{"movie": client, "tv_show": client}, store, itemSvc)
}

func newServiceWith(providers Providers, store *fakeStore, itemSvc *fakeItems) *Service {
	return New(providers, store, itemSvc, fakeSettings{workspace.Settings{
		MetadataLanguage: "it-IT", StreamingCountryCode: "IT", PreferredStore: "kobo",
	}}, testClock{now: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}, uuid.New())
}

func TestCreateDefaultLookupDegradesButExplicitLookupFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lookup   *bool
		client   *fakeClient
		wantCode string
	}{
		{name: "default missing key", client: &fakeClient{}},
		{name: "default upstream failure", client: &fakeClient{enabled: true, err: errors.New("down")}},
		{name: "explicit missing key", lookup: boolPtr(true), client: &fakeClient{}, wantCode: "disabled"},
		{name: "explicit upstream failure", lookup: boolPtr(true), client: &fakeClient{enabled: true, err: errors.New("down")}, wantCode: "upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{typ: "movie"}
			itemSvc := &fakeItems{item: items.Item{Title: "Typed title"}}
			_, err := newService(tc.client, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Typed title"}, CreateOptions{Lookup: tc.lookup})
			switch tc.wantCode {
			case "disabled":
				var target *apperr.LookupDisabledError
				if !errors.As(err, &target) || itemSvc.created != nil {
					t.Fatalf("err=%v created=%v", err, itemSvc.created)
				}
			case "upstream":
				var target *apperr.UpstreamUnavailableError
				if !errors.As(err, &target) || itemSvc.created != nil {
					t.Fatalf("err=%v created=%v", err, itemSvc.created)
				}
			default:
				if err != nil || itemSvc.created == nil || itemSvc.created.Title != "Typed title" || store.applied != nil {
					t.Fatalf("err=%v created=%v applied=%v", err, itemSvc.created, store.applied)
				}
			}
		})
	}
}

func TestCreateOptOutStoresLookupDisabled(t *testing.T) {
	store := &fakeStore{typ: "tv_show"}
	itemSvc := &fakeItems{}
	_, err := newService(&fakeClient{enabled: true}, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{
		Title: "Series", Metadata: map[string]any{"seasons": 2},
	}, CreateOptions{Lookup: boolPtr(false)})
	if err != nil || itemSvc.created == nil || itemSvc.created.Metadata["lookup_disabled"] != true || itemSvc.created.Metadata["seasons"] != 2 {
		t.Fatalf("err=%v input=%+v", err, itemSvc.created)
	}
}

func TestCreateMetadataOptOutSuppressesDefaultLookup(t *testing.T) {
	store := &fakeStore{typ: "movie"}
	itemSvc := &fakeItems{}
	client := &fakeClient{enabled: true, matches: []Suggestion{{SourceID: "7"}}}
	_, err := newService(client, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{
		Title: "Movie", Metadata: map[string]any{"lookup_disabled": true},
	}, CreateOptions{})
	if err != nil || itemSvc.created == nil || client.searchQuery != "" || store.applied != nil {
		t.Fatalf("err=%v created=%v search=%q applied=%v", err, itemSvc.created, client.searchQuery, store.applied)
	}
}

func TestCreateSelectedIDWithoutDetailsReturnsUpstreamUnavailable(t *testing.T) {
	itemSvc := &fakeItems{}
	client := &fakeClient{enabled: true}
	selectedID := 7
	_, err := newService(client, &fakeStore{typ: "movie"}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Movie"}, CreateOptions{TMDBID: &selectedID})
	var unavailable *apperr.UpstreamUnavailableError
	if !errors.As(err, &unavailable) || itemSvc.created != nil {
		t.Fatalf("err=%v created=%v", err, itemSvc.created)
	}
}

func TestCreateUsesFirstLocalizedMatchAndAtomicApply(t *testing.T) {
	client := &fakeClient{enabled: true, matches: []Suggestion{{SourceID: "7"}}, details: Details{
		SourceID: "7", Title: "Titolo locale", Fields: map[string]any{"release_year": 1999},
	}}
	store := &fakeStore{typ: "movie", result: items.Item{Title: "Titolo locale"}}
	itemSvc := &fakeItems{}
	in := items.CreateInput{Title: " titolo cercato ", Metadata: map[string]any{"director": "User"}}
	got, err := newService(client, store, itemSvc).Create(t.Context(), uuid.New(), in, CreateOptions{})
	if err != nil || got.Title != "Titolo locale" || store.applied == nil {
		t.Fatalf("got=%+v err=%v applied=%+v", got, err, store.applied)
	}
	if client.searchType != "movie" || client.searchQuery != in.Title || client.searchLanguage != "it-IT" {
		t.Fatalf("search = %q %q %q", client.searchType, client.searchQuery, client.searchLanguage)
	}
	p := store.applied
	if p.Mode != ModeCreate || p.Provider != ProviderTMDB || p.SourceID != "7" || p.Title != "Titolo locale" || p.OriginalTitle != in.Title || p.CreateInput.Metadata["director"] != "User" {
		t.Fatalf("apply = %+v", p)
	}
}

func TestApplyNoMatchRechecksVersion(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{ID: id, Title: "Unknown", Type: ptr("movie"), Version: 4}}
	store := &fakeStore{}
	got, err := newService(&fakeClient{enabled: true}, store, itemSvc).Apply(t.Context(), id, 4, ModeFill, Selection{})
	if err != nil || got.Version != 4 || itemSvc.updateCalls != 1 || store.applied != nil {
		t.Fatalf("got=%+v err=%v updates=%d applied=%v", got, err, itemSvc.updateCalls, store.applied)
	}
}

func TestApplyRejectsUnsupportedModeBeforeLookup(t *testing.T) {
	// refresh is no longer an API mode (D-123): the weekly refresh replaces it.
	for _, mode := range []Mode{ModeCreate, ModeScheduled, Mode("refresh")} {
		client := &fakeClient{enabled: true}
		_, err := newService(client, &fakeStore{}, &fakeItems{}).Apply(t.Context(), uuid.New(), 1, mode, Selection{})
		var validation *apperr.ValidationError
		if !errors.As(err, &validation) || client.searchQuery != "" {
			t.Errorf("%s: err=%v search=%q", mode, err, client.searchQuery)
		}
	}
}

func TestApplyRejectsStaleSnapshotBeforeNetwork(t *testing.T) {
	itemSvc := &fakeItems{item: items.Item{Type: ptr("movie"), Version: 8}}
	client := &fakeClient{enabled: true}
	_, err := newService(client, &fakeStore{}, itemSvc).Apply(t.Context(), uuid.New(), 7, ModeFill, Selection{})
	var stale *apperr.VersionConflictError
	if !errors.As(err, &stale) || stale.Current != 8 || client.searchQuery != "" {
		t.Fatalf("err=%v search=%q", err, client.searchQuery)
	}
}

func boolPtr(v bool) *bool { return &v }

func ptr(s string) *string { return &s }

// ---- Open Library (books, F016-B3) ----

func bookClient(enabled bool) *fakeClient {
	return &fakeClient{
		name: ProviderOpenLibrary, enabled: enabled,
		matches: []Suggestion{{SourceID: "OL1W"}},
		details: Details{
			SourceID: "OL1W", Title: "Dune", Fields: map[string]any{"author": "Frank Herbert", "pages": 608},
			ImagePath: "123",
		},
	}
}

func bookService(book *fakeClient, tm *fakeClient, store *fakeStore, itemSvc *fakeItems) *Service {
	return newServiceWith(Providers{"book": book, "movie": tm, "tv_show": tm}, store, itemSvc)
}

func TestF016B3_CreateOnBookListUsesTheBookProviderAndPreferredStores(t *testing.T) {
	book, tm := bookClient(true), &fakeClient{enabled: true}
	store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
	in := items.CreateInput{Title: " dune ", Metadata: map[string]any{"author": "Me"}}
	_, err := bookService(book, tm, store, &fakeItems{}).Create(t.Context(), uuid.New(), in, CreateOptions{})
	// The fake cannot download a cover (nil bytes are not an image): a default create then degrades to a plain create.
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if book.searchType != "book" || book.searchQuery != in.Title || book.searchLanguage != "it-IT" || tm.searchQuery != "" {
		t.Fatalf("search = %q %q %q (tmdb searched %q)", book.searchType, book.searchQuery, book.searchLanguage, tm.searchQuery)
	}
}

func TestF016B3_CreateAppliesBookDetailsWithoutCover(t *testing.T) {
	book := bookClient(true)
	book.details.ImagePath = "" // no cover: nothing to download
	store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
	in := items.CreateInput{Title: "dune"}
	got, err := bookService(book, &fakeClient{}, store, &fakeItems{}).Create(t.Context(), uuid.New(), in, CreateOptions{})
	if err != nil || got.Title != "Dune" || store.applied == nil {
		t.Fatalf("got=%+v err=%v applied=%+v", got, err, store.applied)
	}
	p := store.applied
	if p.Mode != ModeCreate || p.Provider != ProviderOpenLibrary || p.SourceID != "OL1W" || p.Type != "book" || p.OriginalTitle != "dune" {
		t.Fatalf("apply = %+v", p)
	}
	if p.Fields["author"] != "Frank Herbert" {
		t.Errorf("fields = %v", p.Fields)
	}
	// The workspace's default store (kobo in the fake settings), no Open Library link.
	if got, want := p.FillOnly["stores"], []string{"kobo"}; !equalStrings(got, want) {
		t.Errorf("fill-only stores = %v, want %v", got, want)
	}
}

func TestF016B3_CreateWithSourceIDSelectsAndIgnoresTMDBID(t *testing.T) {
	book := bookClient(true)
	book.details.ImagePath = ""
	book.details.SourceID = "OL9W"
	store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
	sourceID, tmdbID := "OL9W", 5
	_, err := bookService(book, &fakeClient{}, store, &fakeItems{}).Create(t.Context(), uuid.New(), items.CreateInput{Title: "dune"}, CreateOptions{SourceID: &sourceID, TMDBID: &tmdbID})
	if err != nil || store.applied == nil || store.applied.SourceID != "OL9W" || book.searchQuery != "" || book.detailsID != "OL9W" {
		t.Fatalf("err=%v applied=%+v search=%q details=%q", err, store.applied, book.searchQuery, book.detailsID)
	}
}

func TestF016B3_CreateRejectsMalformedSourceIDBeforeAnyRequest(t *testing.T) {
	book := bookClient(true)
	for _, id := range []string{"603", "OL1", "../x", "OL1W/../x"} {
		itemSvc := &fakeItems{}
		_, err := bookService(book, &fakeClient{}, &fakeStore{typ: "book"}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "dune"}, CreateOptions{SourceID: &id})
		var validation *apperr.ValidationError
		if !errors.As(err, &validation) || book.detailsID != "" || itemSvc.created != nil {
			t.Errorf("source_id %q: err=%v details=%q created=%v", id, err, book.detailsID, itemSvc.created)
		}
	}
}

func TestF016B3_BookCreateDisabledAndOptOut(t *testing.T) {
	// Disabled: a default create degrades, an explicit one is 503 lookup_disabled; TMDB being on does not matter.
	book, tm := bookClient(false), &fakeClient{enabled: true}
	itemSvc := &fakeItems{}
	if _, err := bookService(book, tm, &fakeStore{typ: "book"}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Dune"}, CreateOptions{}); err != nil || itemSvc.created == nil {
		t.Fatalf("default create err=%v created=%v", err, itemSvc.created)
	}
	yes := true
	var disabled *apperr.LookupDisabledError
	if _, err := bookService(book, tm, &fakeStore{typ: "book"}, &fakeItems{}).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Dune"}, CreateOptions{Lookup: &yes}); !errors.As(err, &disabled) {
		t.Fatalf("explicit create err = %v", err)
	}
	// lookup:false stores the opt-out.
	no := false
	itemSvc = &fakeItems{}
	if _, err := bookService(bookClient(true), tm, &fakeStore{typ: "book"}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Dune"}, CreateOptions{Lookup: &no}); err != nil || itemSvc.created.Metadata["lookup_disabled"] != true {
		t.Fatalf("opt-out err=%v input=%+v", err, itemSvc.created)
	}
}

func TestF016B3_UnservedTypesCreateWithoutLookup(t *testing.T) {
	for _, typ := range []string{"", "vehicle", "custom_type"} {
		book, tm := bookClient(true), &fakeClient{enabled: true}
		itemSvc := &fakeItems{}
		if _, err := bookService(book, tm, &fakeStore{typ: typ}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "x"}, CreateOptions{}); err != nil || itemSvc.created == nil || book.searchQuery != "" || tm.searchQuery != "" {
			t.Errorf("type %q: err=%v created=%v", typ, err, itemSvc.created)
		}
	}
}

func TestF016B3_ApplyFillAndRematchPassFillOnlyAndSelectedID(t *testing.T) {
	id := uuid.New()
	for _, mode := range []Mode{ModeFill, ModeRematch} {
		itemSvc := &fakeItems{item: items.Item{ID: id, Title: "dune", Type: ptr("book"), Version: 2, Image: &items.Image{Source: items.ImageSourceURL, URL: "https://x.example/a.jpg"}}}
		book := bookClient(true)
		book.details.SourceID = "OL8W"
		store := &fakeStore{result: itemSvc.item}
		selected := "OL8W"
		if _, err := bookService(book, &fakeClient{}, store, itemSvc).Apply(t.Context(), id, 2, mode, Selection{SourceID: &selected}); err != nil || store.applied == nil {
			t.Fatalf("%s: err=%v", mode, err)
		}
		p := store.applied
		if p.Mode != mode || p.SourceID != "OL8W" || p.OriginalTitle != "dune" || p.FillOnly["stores"] == nil || book.searchQuery != "" {
			t.Errorf("%s: apply = %+v search=%q", mode, p, book.searchQuery)
		}
	}
}

func TestF016B3_ApplyDisabledProviderAndUnservedType(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{ID: id, Title: "Dune", Type: ptr("book"), Version: 1}}
	var disabled *apperr.LookupDisabledError
	// Book lookup off, TMDB on: books are 503 lookup_disabled.
	if _, err := bookService(bookClient(false), &fakeClient{enabled: true}, &fakeStore{}, itemSvc).Apply(t.Context(), id, 1, ModeFill, Selection{}); !errors.As(err, &disabled) {
		t.Errorf("disabled book provider err = %v", err)
	}
	// Everything off: disabled even before the item is read.
	if _, err := bookService(bookClient(false), &fakeClient{}, &fakeStore{}, &fakeItems{err: errors.New("must not be read")}).Apply(t.Context(), id, 1, ModeFill, Selection{}); !errors.As(err, &disabled) {
		t.Errorf("all disabled err = %v", err)
	}
	// A type no provider serves is a validation error on `item`.
	itemSvc = &fakeItems{item: items.Item{ID: id, Title: "Bike", Type: ptr("vehicle"), Version: 1}}
	_, err := bookService(bookClient(true), &fakeClient{enabled: true}, &fakeStore{}, itemSvc).Apply(t.Context(), id, 1, ModeFill, Selection{})
	var validation *apperr.ValidationError
	if !errors.As(err, &validation) || validation.Errors[0].Field != "item" {
		t.Errorf("unserved type err = %v", err)
	}
}

func TestF016B3_MissingCoverStillAppliesTheMatch(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{ID: id, Title: "Dune", Type: ptr("book"), Version: 1}}
	book := bookClient(true)
	book.downloadErr = ErrNoImage
	store := &fakeStore{result: itemSvc.item}
	if _, err := bookService(book, &fakeClient{}, store, itemSvc).Apply(t.Context(), id, 1, ModeFill, Selection{}); err != nil || store.applied == nil || store.applied.Poster != nil || book.downloads != 1 {
		t.Fatalf("err=%v applied=%+v downloads=%d", err, store.applied, book.downloads)
	}
}

func TestF016B3_ProviderErrorMapping(t *testing.T) {
	var disabled *apperr.LookupDisabledError
	var upstream *apperr.UpstreamUnavailableError
	if !errors.As(ProviderError(ErrDisabled), &disabled) || !errors.As(ProviderError(errors.New("boom")), &upstream) {
		t.Fatal("ProviderError must map ErrDisabled to lookup_disabled and anything else to upstream_unavailable")
	}
	// A provider returning ErrDisabled mid-flight is mapped the same way by the service.
	book := bookClient(true)
	book.err = ErrDisabled
	yes := true
	if _, err := bookService(book, &fakeClient{}, &fakeStore{typ: "book"}, &fakeItems{}).Create(t.Context(), uuid.New(), items.CreateInput{Title: "x"}, CreateOptions{Lookup: &yes}); !errors.As(err, &disabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestF016_DefaultStoreFill(t *testing.T) {
	book, movie := &fakeClient{name: ProviderOpenLibrary}, &fakeClient{name: ProviderTMDB}
	for _, tc := range []struct {
		name      string
		provider  Provider
		fill      map[string]any
		preferred string
		want      any
	}{
		{"book gets the default store", book, nil, "kobo", []string{"kobo"}},
		{"book keeps other fill-only values", book, map[string]any{"x": 1}, "amazon", []string{"amazon"}},
		{"movie has no stores field", movie, nil, "kobo", nil},
		{"no preferred store", book, nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultStoreFill(tc.provider, tc.fill, tc.preferred)["stores"]
			if tc.want == nil && got != nil || tc.want != nil && !equalStrings(got, tc.want) {
				t.Errorf("stores = %v, want %v", got, tc.want)
			}
			if _, ok := tc.fill["stores"]; ok {
				t.Errorf("the caller's map was modified: %v", tc.fill)
			}
		})
	}
}

func TestMetadataSourceID(t *testing.T) {
	for _, tc := range []struct {
		provider, raw, want string
	}{
		{ProviderTMDB, `{"tmdb_id":42}`, "42"},
		{ProviderTMDB, `{"tmdb_id":0}`, ""},
		{ProviderTMDB, `{"tmdb_id":1.5}`, ""},
		{ProviderTMDB, `{"tmdb_id":"42"}`, ""},
		{ProviderTMDB, `{"openlibrary_id":"OL1W"}`, ""},
		{ProviderOpenLibrary, `{"openlibrary_id":"OL1W"}`, "OL1W"},
		{ProviderOpenLibrary, `{"openlibrary_id":"nope"}`, ""},
		{ProviderOpenLibrary, `{"tmdb_id":5}`, ""},
		{ProviderOpenLibrary, `not json`, ""},
	} {
		if got := metadataSourceID(tc.provider, json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("metadataSourceID(%s, %s) = %q, want %q", tc.provider, tc.raw, got, tc.want)
		}
	}
}

func TestValidSourceID(t *testing.T) {
	for _, tc := range []struct {
		provider, id string
		ok           bool
	}{
		{ProviderTMDB, "603", true}, {ProviderTMDB, "0", false}, {ProviderTMDB, "-1", false}, {ProviderTMDB, "abc", false},
		{ProviderTMDB, "2147483648", false}, {ProviderTMDB, "2147483647", true},
		{ProviderOpenLibrary, "OL45804W", true}, {ProviderOpenLibrary, "OL45804A", false}, {ProviderOpenLibrary, "ol1w", false},
		{ProviderOpenLibrary, "OL1W\n", false}, {ProviderOpenLibrary, "", false}, {ProviderOpenLibrary, "603", false},
	} {
		if err := ValidSourceID(tc.provider, tc.id); (err == nil) != tc.ok {
			t.Errorf("ValidSourceID(%s, %q) = %v, want ok=%v", tc.provider, tc.id, err, tc.ok)
		}
	}
	if err := ValidSourceID("google", "1"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func equalStrings(got, want any) bool {
	g, ok1 := got.([]string)
	w, ok2 := want.([]string)
	if !ok1 || !ok2 || len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

func TestSelectionPicksTheIDThatFitsTheProvider(t *testing.T) {
	tmdbID, sourceID := 603, "OL1W"
	both := Selection{TMDBID: &tmdbID, SourceID: &sourceID}
	if got := both.forProvider(ProviderTMDB); got == nil || *got != "603" {
		t.Errorf("tmdb selection = %v", got)
	}
	if got := both.forProvider(ProviderOpenLibrary); got == nil || *got != "OL1W" {
		t.Errorf("open library selection = %v", got)
	}
	if got := (Selection{TMDBID: &tmdbID}).forProvider(ProviderOpenLibrary); got != nil {
		t.Errorf("a tmdb_id must be ignored for a book, got %v", *got)
	}
	if got := (Selection{SourceID: &sourceID}).forProvider(ProviderTMDB); got != nil {
		t.Errorf("a source_id must be ignored for a movie, got %v", *got)
	}
}

func TestF016B3_ApplyOnBookIgnoresTMDBIDAndSearchesByTitle(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{ID: id, Title: "dune", Type: ptr("book"), Version: 1, Image: &items.Image{Source: items.ImageSourceURL, URL: "https://x.example/a.jpg"}}}
	book := bookClient(true)
	store := &fakeStore{result: itemSvc.item}
	tmdbID := 603
	if _, err := bookService(book, &fakeClient{}, store, itemSvc).Apply(t.Context(), id, 1, ModeRematch, Selection{TMDBID: &tmdbID}); err != nil || store.applied == nil {
		t.Fatalf("err = %v", err)
	}
	if book.searchQuery != "dune" || store.applied.SourceID != "OL1W" {
		t.Errorf("search = %q applied = %+v", book.searchQuery, store.applied)
	}
}

func TestCreateBookPicksTheCompleteExactTitleMatch(t *testing.T) {
	book := bookClient(true)
	book.details.ImagePath = ""
	book.matches = []Suggestion{
		{SourceID: "OL34961077W", Title: "Il vecchio e il mare"},
		{SourceID: "OL2W", Title: "Il vecchio e il mare", Authors: "Ernest Hemingway", Pages: 127},
	}
	store := &fakeStore{typ: "book", result: items.Item{Title: "Il vecchio e il mare"}}
	_, err := bookService(book, &fakeClient{}, store, &fakeItems{}).Create(t.Context(), uuid.New(),
		items.CreateInput{Title: "il vecchio e il mare"}, CreateOptions{})
	if err != nil || book.detailsID != "OL2W" {
		t.Fatalf("err=%v details id=%q", err, book.detailsID)
	}
}

func TestCreateTMDBStillTakesTheFirstResult(t *testing.T) {
	client := &fakeClient{enabled: true, matches: []Suggestion{{SourceID: "1", Title: "Other"}, {SourceID: "2", Title: "Movie", Authors: "x", Pages: 1}}}
	// The create outcome is irrelevant here (the fake has no poster); only the id searched matters.
	_, _ = newService(client, &fakeStore{typ: "movie"}, &fakeItems{}).Create(t.Context(), uuid.New(),
		items.CreateInput{Title: "Movie"}, CreateOptions{})
	if client.detailsID != "1" {
		t.Fatalf("details id = %q, want 1", client.detailsID)
	}
}
