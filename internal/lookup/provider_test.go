package lookup

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/openlibrary"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
)

func openLibraryFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "openlibrary", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openLibraryProviderFor(t *testing.T, handler http.HandlerFunc, mock bool) Provider {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return NewOpenLibraryProvider(openlibrary.NewWithOptions(openlibrary.Options{
		APIBaseURL: s.URL, CoverBaseURL: s.URL, HTTPClient: s.Client(), RequestInterval: time.Millisecond, MockFixture: mock,
	}))
}

func TestOpenLibraryProviderSearchMapsSuggestions(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(openLibraryFixture(t, "search_english.json"))
	}, false)
	got, err := p.Search(t.Context(), "book", "the hobbit", "en-US")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v err %v", got, err)
	}
	first := got[0]
	if first.SourceID != "OL27482W" || first.Title != "The Hobbit" || first.Authors != "J.R.R. Tolkien" || first.Year != 1937 ||
		first.Pages != 310 || first.ISBN != "9780261103283" || !strings.HasSuffix(first.ImageURL, "/b/id/14627509-M.jpg") {
		t.Errorf("first = %+v", first)
	}
	if got[1].Authors != "J.R.R. Tolkien, Alan Lee" || got[1].ImageURL != "" || got[1].Pages != 0 {
		t.Errorf("second = %+v", got[1])
	}
	if p.Name() != ProviderOpenLibrary || !p.Enabled() {
		t.Errorf("name=%q enabled=%v", p.Name(), p.Enabled())
	}
}

func TestOpenLibraryProviderDetails(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "key:/works/OL27482W" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(openLibraryFixture(t, "work_hobbit.json"))
	}, false)
	d, err := p.Details(t.Context(), "book", "OL27482W", "it-IT", "IT")
	if err != nil {
		t.Fatal(err)
	}
	wantFields := map[string]any{"author": "J.R.R. Tolkien", "pages": 310, "isbn": "9780261103283"}
	if d.SourceID != "OL27482W" || d.Title != "The Hobbit" || !reflect.DeepEqual(d.Fields, wantFields) || d.ImagePath != "14627509" {
		t.Errorf("details = %+v", d)
	}
	if d.FillOnly != nil {
		t.Errorf("fill-only = %v, want none (the default store is added by the service)", d.FillOnly)
	}
	if _, err := p.Details(t.Context(), "movie", "OL27482W", "it-IT", ""); err == nil {
		t.Error("a non-book type was accepted")
	}
}

func TestOpenLibraryProviderDownloadImage(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b/id/7-L.jpg" {
			_, _ = w.Write([]byte("img"))
			return
		}
		http.NotFound(w, r)
	}, false)
	if b, err := p.DownloadImage(t.Context(), "7"); err != nil || string(b) != "img" {
		t.Fatalf("b=%q err=%v", b, err)
	}
	if _, err := p.DownloadImage(t.Context(), "8"); !errors.Is(err, ErrNoImage) {
		t.Errorf("a missing cover must be ErrNoImage, got %v", err)
	}
	if _, err := p.DownloadImage(t.Context(), "../x"); err == nil {
		t.Error("a non-numeric image path was accepted")
	}
}

func TestOpenLibraryProviderDisabledMapsToErrDisabled(t *testing.T) {
	p := NewOpenLibraryProvider(openlibrary.NewWithOptions(openlibrary.Options{Disabled: true}))
	if p.Enabled() {
		t.Fatal("disabled provider is enabled")
	}
	if _, err := p.Search(t.Context(), "book", "x", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Search err = %v", err)
	}
	if _, err := p.Details(t.Context(), "book", "OL1W", "", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Details err = %v", err)
	}
	if _, err := p.DownloadImage(t.Context(), "1"); !errors.Is(err, ErrDisabled) {
		t.Errorf("DownloadImage err = %v", err)
	}
	if err := p.ValidateSourceID("OL1W"); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
	if err := p.ValidateSourceID("603"); err == nil {
		t.Error("a TMDB-style id was accepted as a work id")
	}
}

func TestOpenLibraryProviderFixtureNeedsNoNetwork(t *testing.T) {
	p := openLibraryProviderFor(t, func(http.ResponseWriter, *http.Request) { t.Error("the fixture made a request") }, true)
	got, err := p.Search(t.Context(), "book", "[mock] book name", "it-IT")
	if err != nil || len(got) != 1 || got[0].SourceID != "OL900001W" || got[0].Title != "Mock Book Title" || got[0].Authors != "Mock Author" {
		t.Fatalf("got %+v err %v", got, err)
	}
	d, err := p.Details(t.Context(), "book", "OL900001W", "it-IT", "IT")
	if err != nil || d.Fields["pages"] != 321 || d.Fields["isbn"] != "9780000000002" || d.Fields["author"] != "Mock Author" || d.ImagePath == "" {
		t.Fatalf("details %+v err %v", d, err)
	}
	cover, err := p.DownloadImage(t.Context(), d.ImagePath)
	if err != nil || len(cover) == 0 {
		t.Fatalf("cover len=%d err=%v", len(cover), err)
	}
}

func TestJoinAuthorsAndTruncate(t *testing.T) {
	long := strings.Repeat("é", 150)
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"none", nil, ""},
		{"one", []string{"Frank Herbert"}, "Frank Herbert"},
		{"joined", []string{"A", "B", "C"}, "A, B, C"},
		{"whole names only", []string{strings.Repeat("a", 60), strings.Repeat("b", 60)}, strings.Repeat("a", 60)},
		{"one over-long name is cut by runes", []string{long}, strings.Repeat("é", 100)},
	} {
		if got := joinAuthors(tc.in); got != tc.want {
			t.Errorf("%s: joinAuthors = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := truncateRunes(strings.Repeat("x", 400), 300); len(got) != 300 {
		t.Errorf("truncateRunes length = %d", len(got))
	}
	if got := truncateRunes("short", 300); got != "short" {
		t.Errorf("truncateRunes = %q", got)
	}
}

func TestTMDBProviderAdapter(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/movie":
			_, _ = w.Write([]byte(`{"results":[{"id":603,"title":"The Matrix","original_title":"The Matrix","release_date":"1999-03-31","poster_path":"/m.jpg","overview":"Neo."}]}`))
		case "/3/movie/603":
			_, _ = w.Write([]byte(`{"id":603,"title":"The Matrix","release_date":"1999-03-31","runtime":136,"poster_path":"/m.jpg","credits":{"crew":[{"job":"Director","name":"Lana"}]}}`))
		case "/w500/m.jpg":
			_, _ = w.Write([]byte("poster"))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(s.Close)
	p := NewTMDBProvider(tmdb.NewWithOptions("key", tmdb.Options{APIBaseURL: s.URL + "/3", ImageBaseURL: s.URL, HTTPClient: s.Client()}))
	if p.Name() != ProviderTMDB || !p.Enabled() {
		t.Fatalf("name=%q enabled=%v", p.Name(), p.Enabled())
	}
	got, err := p.Search(t.Context(), "movie", "matrix", "en-US")
	if err != nil || len(got) != 1 || got[0].SourceID != "603" || got[0].Year != 1999 || got[0].Overview != "Neo." ||
		got[0].OriginalTitle != "The Matrix" || !strings.HasSuffix(got[0].ImageURL, "/w185/m.jpg") {
		t.Fatalf("suggestions %+v err %v", got, err)
	}
	d, err := p.Details(t.Context(), "movie", "603", "en-US", "")
	if err != nil || d.SourceID != "603" || d.Title != "The Matrix" || d.ImagePath != "/m.jpg" ||
		d.Fields["runtime_minutes"] != 136 || d.Fields["director"] != "Lana" || d.FillOnly != nil {
		t.Fatalf("details %+v err %v", d, err)
	}
	if b, err := p.DownloadImage(t.Context(), "/m.jpg"); err != nil || string(b) != "poster" {
		t.Errorf("poster %q err %v", b, err)
	}
	if _, err := p.Details(t.Context(), "movie", "x", "en-US", ""); err == nil {
		t.Error("a non-numeric id was accepted")
	}
	if err := p.ValidateSourceID("0"); err == nil {
		t.Error("id 0 accepted")
	}
}

func TestTMDBProviderDisabledMapsToErrDisabled(t *testing.T) {
	p := NewTMDBProvider(tmdb.New(""))
	if p.Enabled() {
		t.Fatal("keyless provider enabled")
	}
	if _, err := p.Search(t.Context(), "movie", "x", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Search err = %v", err)
	}
	if _, err := p.Details(t.Context(), "movie", "1", "", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Details err = %v", err)
	}
	if _, err := p.DownloadImage(t.Context(), "/p.jpg"); !errors.Is(err, ErrDisabled) {
		t.Errorf("DownloadImage err = %v", err)
	}
	if NewTMDBProvider(nil).Enabled() {
		t.Error("nil client enabled")
	}
}

func TestTMDBMockFixtureStillWorksThroughTheAdapter(t *testing.T) {
	p := NewTMDBProvider(tmdb.NewWithOptions("", tmdb.Options{MockFixture: true}))
	got, err := p.Search(t.Context(), "movie", "[mock] movie name", "en-US")
	if err != nil || len(got) != 1 || got[0].SourceID != "900001" {
		t.Fatalf("got %+v err %v", got, err)
	}
	d, err := p.Details(t.Context(), "movie", got[0].SourceID, "en-US", "")
	if err != nil || d.Fields["director"] != "Mock Director" || d.Fields["runtime_minutes"] != 101 {
		t.Fatalf("details %+v err %v", d, err)
	}
}

func TestRetentionIsDeclaredOnlyByTMDB(t *testing.T) {
	if r := retentionFor(ProviderTMDB); r == nil || r.RefreshMonths != 5 || r.ExpireMonths != 6 {
		t.Errorf("tmdb retention = %+v, want 5/6 months (D-089)", r)
	}
	if r := retentionFor(ProviderOpenLibrary); r != nil {
		t.Errorf("openlibrary retention = %+v, want none (D-095)", r)
	}
}
