package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/openlibrary"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/tree"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// F016-B3: book lookup over HTTP. Open Library is always a local fake server (or the
// in-process fixture); no test reaches the real network.

func olFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "openlibrary", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func coverPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: uint8(40 * x), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

type bookEnv struct {
	*treeEnv
	searches, covers *atomic.Int32
	lastQuery        atomic.Value // url.Values of the last search
}

type bookEnvOptions struct {
	disabled bool // Open Library turned off
	mock     bool // the TMDB_MOCK_FIXTURE flag
	noBooks  bool // no Books provider wired at all
	upstream http.HandlerFunc
	cover    http.HandlerFunc // overrides the cover answer
	timeout  time.Duration    // per-request timeout of the client
}

func newBookEnv(t *testing.T, o bookEnvOptions) *bookEnv {
	t.Helper()
	ctx := t.Context()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws, err := workspace.Bootstrap(ctx, pool, "W")
	if err != nil {
		t.Fatal(err)
	}
	e := &bookEnv{searches: new(atomic.Int32), covers: new(atomic.Int32)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search.json":
			e.searches.Add(1)
			e.lastQuery.Store(r.URL.Query())
			if o.upstream != nil {
				o.upstream(w, r)
				return
			}
			switch r.URL.Query().Get("q") {
			case "key:/works/OL27482W":
				_, _ = w.Write(olFixture(t, "work_hobbit.json"))
			case "dun frank herbart":
				_, _ = w.Write(olFixture(t, "search_misspelling.json"))
			default:
				_, _ = w.Write(olFixture(t, "search_english.json"))
			}
		case strings.HasPrefix(r.URL.Path, "/b/id/"):
			e.covers.Add(1)
			if o.cover != nil {
				o.cover(w, r)
				return
			}
			_, _ = w.Write(coverPNG())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	client := openlibrary.NewWithOptions(openlibrary.Options{
		APIBaseURL: upstream.URL, CoverBaseURL: upstream.URL, HTTPClient: upstream.Client(),
		RequestInterval: time.Millisecond, Timeout: o.timeout, Disabled: o.disabled, MockFixture: o.mock,
	})
	books := lookup.NewOpenLibraryProvider(client)

	dataDir := t.TempDir()
	files, err := storage.NewLocal(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	itemSvc := items.New(pool, clock.System{}, ws)
	settingsSvc := workspace.NewService(pool)
	store := lookup.NewStore(pool, files, clock.System{}, itemSvc, quietLogger(), ws)
	svc := lookup.New(lookup.Providers{types.Book: books}, store, itemSvc, settingsSvc, clock.System{}, ws)
	deps := httpapi.Deps{
		DB: pool, DataDir: dataDir, Logger: quietLogger(), WorkspaceID: ws, Settings: settingsSvc,
		Definitions: definitions.New(pool, ws), Tree: tree.New(pool, clock.System{}, ws), Items: itemSvc,
		Search: search.New(pool, ws), Lookup: svc, BookLookupAvailable: books.Enabled(),
	}
	if !o.noBooks {
		deps.Books = books
	}
	e.treeEnv = &treeEnv{t: t, h: newRouter(t, deps), pool: pool, ws: ws}
	return e
}

func (e *bookEnv) bookList() string { return e.listOfType("Books", nil, "book")["id"].(string) }

func (e *bookEnv) suggest(q string) *httptest.ResponseRecorder {
	return e.call(http.MethodGet, "/api/v1/books/suggestions?q="+url.QueryEscape(q), "", "")
}

func (e *bookEnv) lookupItem(it obj, body string) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/api/v1/items/"+it["id"].(string)+"/lookup", body, fmt.Sprintf(`"i-%v"`, it["version"]))
}

func metaOf(it obj) map[string]any { return it["metadata"].(map[string]any) }

func TestF016B3_BookSuggestions(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	rec := e.suggest("the hobbit")
	wantStatus(t, rec, http.StatusOK)
	var got struct {
		Available bool `json:"available"`
		Results   []struct {
			SourceID string  `json:"source_id"`
			Title    string  `json:"title"`
			Authors  string  `json:"authors"`
			Year     *int    `json:"year"`
			Pages    *int    `json:"pages"`
			CoverURL *string `json:"cover_url"`
			ISBN     *string `json:"isbn"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Results) != 2 || got.Results[0].SourceID != "OL27482W" || got.Results[1].SourceID != "OL219602W" {
		t.Fatalf("suggestions = %s", rec.Body)
	}
	first := got.Results[0]
	if first.Title != "The Hobbit" || first.Authors != "J.R.R. Tolkien" || first.Year == nil || *first.Year != 1937 || first.Pages == nil || *first.Pages != 310 ||
		first.CoverURL == nil || !strings.HasSuffix(*first.CoverURL, "/b/id/14627509-M.jpg") || first.ISBN == nil || *first.ISBN != "9780261103283" {
		t.Errorf("first = %+v", first)
	}
	second := got.Results[1]
	if second.Authors != "J.R.R. Tolkien, Alan Lee" || second.Pages != nil || second.CoverURL != nil {
		t.Errorf("sparse result = %+v", second)
	}
	// Null fields are present as null, not omitted or zero.
	var raw struct{ Results []map[string]any }
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if v, ok := raw.Results[1]["pages"]; !ok || v != nil {
		t.Errorf("pages of the sparse result = %v (present=%v), want null", v, ok)
	}
	// The workspace metadata language (it-IT by default) is passed as a two-letter preference.
	if q := e.lastQuery.Load().(url.Values); q.Get("lang") != "it" || q.Get("q") != "the hobbit" {
		t.Errorf("upstream query = %v", q)
	}
}

func TestF016B3_BookSuggestionsByISBNAndNoResults(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	wantStatus(t, e.suggest("978-0-441-17271-9"), http.StatusOK)
	if q := e.lastQuery.Load().(url.Values); q.Get("q") != "isbn:9780441172719" {
		t.Errorf("ISBN query sent upstream = %q", q.Get("q"))
	}
	rec := e.suggest("dun frank herbart")
	wantStatus(t, rec, http.StatusOK)
	if got := decode(t, rec); got["available"] != true || len(got["results"].([]any)) != 0 {
		t.Errorf("a misspelling must give available with no results: %v", got)
	}
}

func TestF016B3_BookSuggestionsDisabled(t *testing.T) {
	for name, o := range map[string]bookEnvOptions{
		"turned off":     {disabled: true},
		"not configured": {noBooks: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newBookEnv(t, o)
			rec := e.suggest("the hobbit")
			wantStatus(t, rec, http.StatusOK)
			if got := decode(t, rec); got["available"] != false || len(got["results"].([]any)) != 0 {
				t.Fatalf("disabled suggestions = %v", got)
			}
			if e.searches.Load() != 0 {
				t.Error("a disabled lookup reached the upstream")
			}
		})
	}
}

func TestF016B3_BookSuggestionsRejectBadQuery(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	wantProblem(t, e.suggest("   "), http.StatusBadRequest, "invalid_parameter")
	wantProblem(t, e.call(http.MethodGet, "/api/v1/books/suggestions", "", ""), http.StatusBadRequest, "invalid_parameter")
	wantProblem(t, e.suggest(strings.Repeat("x", 301)), http.StatusBadRequest, "invalid_parameter")
	if e.searches.Load() != 0 {
		t.Error("a bad query reached the upstream")
	}
}

func TestF016B3_BookSuggestionsSanitizeUpstreamFailure(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{upstream: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("private upstream diagnostic"))
	}})
	rec := e.suggest("boom")
	p := wantProblem(t, rec, http.StatusServiceUnavailable, "upstream_unavailable")
	if body := rec.Body.String(); strings.Contains(body, "diagnostic") || strings.Contains(p.Detail, "502") || strings.Contains(body, "openlibrary") {
		t.Fatalf("problem leaks upstream details: %s", body)
	}
}

func TestF016B3_FixtureServesOnlyTheExactQueryWithoutNetwork(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{mock: true})
	rec := e.suggest("[mock] book name")
	wantStatus(t, rec, http.StatusOK)
	got := decode(t, rec)
	results := got["results"].([]any)
	if got["available"] != true || len(results) != 1 {
		t.Fatalf("fixture suggestions = %v", got)
	}
	r := results[0].(map[string]any)
	if r["source_id"] != "OL900001W" || r["title"] != "Mock Book Title" || r["authors"] != "Mock Author" || r["pages"] != float64(321) ||
		r["year"] != float64(2002) || r["isbn"] != "9780000000002" {
		t.Errorf("fixture result = %v", r)
	}
	if e.searches.Load() != 0 {
		t.Fatalf("the fixture made %d upstream requests", e.searches.Load())
	}
	// Any other query is a normal upstream query.
	wantStatus(t, e.suggest("the hobbit"), http.StatusOK)
	if e.searches.Load() != 1 {
		t.Errorf("a non-fixture query must reach the upstream, searches = %d", e.searches.Load())
	}
}

func TestF016B3_CreateBookWithLookupFillsFieldsCoverAndStores(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	e.exec(`UPDATE workspace_settings SET preferred_store = 'kobo' WHERE workspace_id = $1`, e.ws)
	lid := e.bookList()

	it := e.newItem(lid, `{"title":"the hobbit","metadata":{"pages":999}}`)
	if it["title"] != "the hobbit" || it["type"] != "book" || it["image"] == nil {
		t.Fatalf("item = %v", it)
	}
	meta := metaOf(it)
	if meta["author"] != "J.R.R. Tolkien" || meta["pages"] != float64(999) ||
		meta["isbn"] != "9780261103283" || meta["openlibrary_id"] != "OL27482W" {
		t.Errorf("metadata = %v", meta)
	}
	if _, has := meta["first_publish_year"]; has {
		t.Errorf("lookup must not fill first_publish_year: %v", meta)
	}
	// The workspace's default store, no Open Library link.
	if !reflect.DeepEqual(meta["stores"], []any{"kobo"}) {
		t.Errorf("stores = %v", meta["stores"])
	}
	if links, _ := meta["store_links"].(map[string]any); links["kobo"] == nil || links["openlibrary"] != nil {
		t.Errorf("store_links = %v", meta["store_links"])
	}
	if e.covers.Load() != 1 {
		t.Errorf("covers downloaded = %d, want one per imported item", e.covers.Load())
	}
}

// The typed title wins over the provider title (here "The Hobbit"), the lookup still fills the rest.
func TestF016B3_CreateBookKeepsTypedTitleWhenLookupFills(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	it := e.newItem(e.bookList(), `{"title":"lo hobbit"}`)
	meta := metaOf(it)
	if it["title"] != "lo hobbit" || meta["author"] != "J.R.R. Tolkien" || meta["pages"] != float64(310) || meta["openlibrary_id"] != "OL27482W" {
		t.Fatalf("item = %v", it)
	}
}

func TestF016B3_CreateBookWithSourceIDAndOptOut(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	lid := e.bookList()

	// A selected work id skips the title search; a tmdb_id on a book list is ignored.
	it := e.newItem(lid, `{"title":"my own title","source_id":"OL27482W","tmdb_id":603}`)
	if it["title"] != "my own title" || metaOf(it)["openlibrary_id"] != "OL27482W" {
		t.Fatalf("item = %v", it)
	}
	if q := e.lastQuery.Load().(url.Values); q.Get("q") != "key:/works/OL27482W" {
		t.Errorf("upstream query = %q, want a lookup by work id", q.Get("q"))
	}

	before := e.searches.Load()
	off := e.newItem(lid, `{"title":"untouched","lookup":false}`)
	if off["title"] != "untouched" || metaOf(off)["lookup_disabled"] != true || metaOf(off)["author"] != nil || e.searches.Load() != before {
		t.Errorf("lookup:false item = %v (searches %d -> %d)", off, before, e.searches.Load())
	}

	// A malformed work id is a 422 and creates nothing.
	rec := e.call(http.MethodPost, "/api/v1/lists/"+lid+"/items", `{"title":"x","source_id":"603"}`, "")
	p := wantProblem(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	if len(p.Errors) == 0 || p.Errors[0].Field != "source_id" {
		t.Errorf("errors = %+v", p.Errors)
	}
	// No match (a misspelling) still creates the item unchanged.
	if none := e.newItem(lid, `{"title":"dun frank herbart"}`); none["title"] != "dun frank herbart" || none["image"] != nil {
		t.Errorf("no-match item = %v", none)
	}
}

func TestF016B3_CreateBookWithExplicitStoresRejectsPrice(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	lid := e.bookList()
	it := e.newItem(lid, `{"title":"the hobbit","metadata":{"stores":["bookshop"]}}`)
	if it["price"] != nil || !reflect.DeepEqual(metaOf(it)["stores"], []any{"bookshop"}) {
		t.Errorf("price/stores = %v / %v", it["price"], metaOf(it)["stores"])
	}
	wantProblem(t, e.call(http.MethodPost, "/api/v1/lists/"+lid+"/items", `{"title":"the hobbit","price":{"amount":"9.9","currency":"EUR"}}`, ""), http.StatusUnprocessableEntity, "validation_failed")
}

func TestF016B3_CreateBookWithFixtureNeedsNoNetwork(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{mock: true})
	lid := e.bookList()
	it := e.newItem(lid, `{"title":"[mock] book name"}`)
	meta := metaOf(it)
	if it["title"] != "[mock] book name" || meta["author"] != "Mock Author" || meta["pages"] != float64(321) ||
		meta["isbn"] != "9780000000002" || meta["openlibrary_id"] != "OL900001W" || it["image"] == nil {
		t.Fatalf("fixture item = %v", it)
	}
	if e.searches.Load() != 0 || e.covers.Load() != 0 {
		t.Errorf("the fixture reached the upstream: searches=%d covers=%d", e.searches.Load(), e.covers.Load())
	}
}

func TestF016B3_LookupFillKeepsUserValuesAndStores(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	lid := e.bookList()
	it := e.newItem(lid, `{"title":"hobbit","lookup":false,"metadata":{"author":"Mine","stores":["kobo"]}}`)

	rec := e.lookupItem(it, `{"mode":"fill","source_id":"OL27482W"}`)
	wantStatus(t, rec, http.StatusOK)
	got := decode(t, rec)
	meta := metaOf(got)
	if meta["author"] != "Mine" || meta["pages"] != float64(310) || meta["openlibrary_id"] != "OL27482W" || !reflect.DeepEqual(meta["stores"], []any{"kobo"}) {
		t.Errorf("metadata after fill = %v", meta)
	}
	if got["title"] != "hobbit" || got["image"] == nil {
		t.Errorf("fill must keep the user's title and add the cover: %v", got)
	}
	if rec.Header().Get("ETag") != fmt.Sprintf(`"i-%v"`, got["version"]) {
		t.Errorf("ETag = %q for version %v", rec.Header().Get("ETag"), got["version"])
	}

	// With no stores at all, fill adds the default store.
	e.exec(`UPDATE workspace_settings SET preferred_store = 'bookshop' WHERE workspace_id = $1`, e.ws)
	bare := e.newItem(lid, `{"title":"hobbit 2","lookup":false,"metadata":{"stores":[]}}`)
	filled := decode(t, e.lookupItem(bare, `{"source_id":"OL27482W"}`))
	if !reflect.DeepEqual(metaOf(filled)["stores"], []any{"bookshop"}) {
		t.Errorf("stores after fill on an empty list = %v", metaOf(filled)["stores"])
	}
}

func TestF016B3_LookupRematchAndRefresh(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{})
	lid := e.bookList()
	it := e.newItem(lid, `{"title":"hobbit","lookup":false,"metadata":{"stores":["kobo"]}}`)

	rec := e.lookupItem(it, `{"mode":"rematch","source_id":"OL27482W"}`)
	wantStatus(t, rec, http.StatusOK)
	got := decode(t, rec)
	if got["title"] != "The Hobbit" || metaOf(got)["author"] != "J.R.R. Tolkien" || !reflect.DeepEqual(metaOf(got)["stores"], []any{"kobo"}) {
		t.Fatalf("after rematch = %v", got)
	}

	// Refresh needs no id: it uses the stored metadata.openlibrary_id.
	before := e.searches.Load()
	rec = e.lookupItem(got, `{"mode":"refresh"}`)
	wantStatus(t, rec, http.StatusOK)
	if q := e.lastQuery.Load().(url.Values); q.Get("q") != "key:/works/OL27482W" || e.searches.Load() != before+1 {
		t.Errorf("refresh query = %q", q.Get("q"))
	}

	// An item that was never matched has nothing to refresh by.
	plain := e.newItem(lid, `{"title":"plain","lookup":false}`)
	p := wantProblem(t, e.lookupItem(plain, `{"mode":"refresh"}`), http.StatusUnprocessableEntity, "validation_failed")
	if len(p.Errors) == 0 || p.Errors[0].Field != "source_id" {
		t.Errorf("errors = %+v", p.Errors)
	}
	// A stale version is rejected before any request.
	stale := obj{"id": it["id"], "version": float64(1)}
	searches := e.searches.Load()
	wantProblem(t, e.lookupItem(stale, `{"mode":"fill"}`), http.StatusPreconditionFailed, "version_conflict")
	if e.searches.Load() != searches {
		t.Error("a stale lookup reached the upstream")
	}
	wantProblem(t, e.call(http.MethodPost, "/api/v1/items/"+plain["id"].(string)+"/lookup", `{"mode":"fill"}`, ""), http.StatusPreconditionRequired, "precondition_required")
}

func TestF016B3_LookupDisabledAndUnservedItems(t *testing.T) {
	// Book lookup off: an explicit lookup is 503 lookup_disabled and a plain create still works.
	e := newBookEnv(t, bookEnvOptions{disabled: true})
	lid := e.bookList()
	it := e.newItem(lid, `{"title":"hobbit"}`)
	if it["title"] != "hobbit" || metaOf(it)["openlibrary_id"] != nil {
		t.Fatalf("a default create with lookup off must be unchanged: %v", it)
	}
	wantProblem(t, e.lookupItem(it, `{"mode":"fill"}`), http.StatusServiceUnavailable, "lookup_disabled")
	wantProblem(t, e.call(http.MethodPost, "/api/v1/lists/"+lid+"/items", `{"title":"x","lookup":true}`, ""), http.StatusServiceUnavailable, "lookup_disabled")

	// An item of a type no provider serves is a validation error.
	on := newBookEnv(t, bookEnvOptions{})
	cars := on.listOfType("Cars", nil, "vehicle")["id"].(string)
	car := on.newItem(cars, `{"title":"Panda"}`)
	wantProblem(t, on.lookupItem(car, `{"mode":"fill"}`), http.StatusUnprocessableEntity, "validation_failed")
}

func TestF016B3_LookupUpstreamFailureIs503(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{upstream: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }})
	lid := e.bookList()
	// A default create degrades to an unchanged item; an explicit lookup is 503.
	if it := e.newItem(lid, `{"title":"hobbit"}`); it["title"] != "hobbit" {
		t.Errorf("default create = %v", it)
	}
	wantProblem(t, e.call(http.MethodPost, "/api/v1/lists/"+lid+"/items", `{"title":"hobbit","lookup":true}`, ""), http.StatusServiceUnavailable, "upstream_unavailable")
}

func TestF016B3_SettingsReportBookLookupAvailability(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    bookEnvOptions
		want bool
	}{{"default on", bookEnvOptions{}, true}, {"turned off", bookEnvOptions{disabled: true}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBookEnv(t, tc.o)
			rec := e.call(http.MethodGet, "/api/v1/settings", "", "")
			wantStatus(t, rec, http.StatusOK)
			got := decode(t, rec)
			if got["book_lookup_available"] != tc.want || got["lookup_available"] != false {
				t.Errorf("settings = %v", got)
			}
		})
	}
}

// The network-free test stack: book lookup disabled, fixture flag on. The fixture
// query works end to end, every other query is the normal disabled state.
func TestF016B3_DisabledWithFixtureServesOnlyTheFixture(t *testing.T) {
	e := newBookEnv(t, bookEnvOptions{disabled: true, mock: true})
	rec := e.suggest("[mock] book name")
	wantStatus(t, rec, http.StatusOK)
	if got := decode(t, rec); got["available"] != true || len(got["results"].([]any)) != 1 {
		t.Fatalf("fixture suggestions = %v", got)
	}
	other := e.suggest("the hobbit")
	wantStatus(t, other, http.StatusOK)
	if got := decode(t, other); got["available"] != false || len(got["results"].([]any)) != 0 {
		t.Fatalf("non-fixture suggestions = %v", got)
	}
	lid := e.bookList()
	if it := e.newItem(lid, `{"title":"[mock] book name"}`); it["title"] != "[mock] book name" {
		t.Errorf("fixture create = %v", it)
	}
	if it := e.newItem(lid, `{"title":"the hobbit"}`); it["title"] != "the hobbit" {
		t.Errorf("non-fixture create must stay unchanged: %v", it)
	}
	if e.searches.Load() != 0 || e.covers.Load() != 0 {
		t.Errorf("network used: searches=%d covers=%d", e.searches.Load(), e.covers.Load())
	}
}

func (e *bookEnv) itemCount() int {
	e.t.Helper()
	var n int
	e.queryRow(`SELECT count(*) FROM items`, nil, &n)
	return n
}

func TestF016_BookCoverFailureKeepsTheMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cover     http.HandlerFunc
		body      string
		wantTitle string
	}{
		{"cover 500 and lookup omitted", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, `{"title":"the hobbit"}`, "the hobbit"},
		{"cover 500 and lookup true", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, `{"title":"the hobbit","lookup":true}`, "the hobbit"},
		{"bad cover bytes and lookup omitted", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not an image")) }, `{"title":"the hobbit"}`, "the hobbit"},
		{"bad cover bytes and lookup true", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not an image")) }, `{"title":"the hobbit","lookup":true}`, "the hobbit"},
		{"cover 302 to a foreign host and a source id", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.invalid/cover.jpg", http.StatusFound)
		}, `{"title":"x","source_id":"OL27482W"}`, "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBookEnv(t, bookEnvOptions{cover: tc.cover})
			it := e.newItem(e.bookList(), tc.body)
			meta := metaOf(it)
			if it["title"] != tc.wantTitle || it["image"] != nil || meta["author"] != "J.R.R. Tolkien" || meta["pages"] != float64(310) || meta["openlibrary_id"] != "OL27482W" {
				t.Fatalf("item = %v", it)
			}
		})
	}
}

func TestF016_BookSearchFailure(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}
	for name, up := range map[string]http.HandlerFunc{
		"search errors":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"search times out": slow,
	} {
		t.Run(name, func(t *testing.T) {
			e := newBookEnv(t, bookEnvOptions{upstream: up, timeout: 40 * time.Millisecond})
			lid := e.bookList()
			// lookup omitted: the item is created unchanged.
			it := e.newItem(lid, `{"title":"the hobbit"}`)
			if it["title"] != "the hobbit" || metaOf(it)["author"] != nil || metaOf(it)["pages"] != nil || it["image"] != nil {
				t.Fatalf("item = %v", it)
			}
			if e.itemCount() != 1 {
				t.Fatalf("items = %d", e.itemCount())
			}
			// lookup:true: 503 and nothing is created.
			rec := e.call(http.MethodPost, "/api/v1/lists/"+lid+"/items", `{"title":"the hobbit 2","lookup":true}`, "")
			wantProblem(t, rec, http.StatusServiceUnavailable, "upstream_unavailable")
			if e.itemCount() != 1 {
				t.Errorf("items = %d after a failed explicit lookup, want 1", e.itemCount())
			}
		})
	}
}
