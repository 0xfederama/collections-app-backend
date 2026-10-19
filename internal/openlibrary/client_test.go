package openlibrary

import (
	"bytes"
	"context"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeServer answers like Open Library from the fixture files. The count is the
// number of requests that reached the server. None of these tests use the network.
func fakeServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &count
}

func testClient(s *httptest.Server, opts Options) *Client {
	opts.APIBaseURL, opts.CoverBaseURL, opts.HTTPClient = s.URL, s.URL, s.Client()
	if opts.RequestInterval == 0 {
		opts.RequestInterval = time.Millisecond
	}
	return NewWithOptions(opts)
}

// TestB1SampleTitles covers the six F016-B1 cases against recorded response shapes.
func TestB1SampleTitles(t *testing.T) {
	for _, tc := range []struct {
		name, query, language, file string
		wantQ, wantLang             string
		wantTitle, wantAuthor       string
		wantYear, wantPages         int
		wantISBN                    string
		wantCover                   int
		wantResults                 int
	}{
		{"english", "the hobbit", "en-US", "search_english.json", "the hobbit", "en", "The Hobbit", "J.R.R. Tolkien", 1937, 310, "9780261103283", 14627509, 2},
		{"italian", "il nome della rosa", "it-IT", "search_italian.json", "il nome della rosa", "it", "Il nome della rosa", "Umberto Eco", 1980, 533, "9788845248061", 8374611, 1},
		{"french", "le petit prince", "fr-FR", "search_french.json", "le petit prince", "fr", "Le petit prince", "Antoine de Saint-Exupéry", 1943, 96, "2070408507", 10527843, 1},
		{"isbn with hyphens", "978-0-441-17271-9", "en-US", "search_isbn.json", "isbn:9780441172719", "en", "Dune", "Frank Herbert", 1965, 608, "9780441172719", 12547191, 1},
		{"bare isbn 10", "0441172717", "", "search_isbn.json", "isbn:0441172717", "", "Dune", "Frank Herbert", 1965, 608, "9780441172719", 12547191, 1},
		{"translated title", "lo hobbit", "it-IT", "search_translated.json", "lo hobbit", "it", "The Hobbit", "J.R.R. Tolkien", 1937, 310, "9788845900815", 14627509, 1},
		{"misspelling finds nothing", "dun frank herbart", "en-US", "search_misspelling.json", "dun frank herbart", "en", "", "", 0, 0, "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, count := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.URL.Path != "/search.json" || q.Get("q") != tc.wantQ || q.Get("lang") != tc.wantLang ||
					q.Get("limit") != "10" || q.Get("fields") != "key,title,author_name,first_publish_year,number_of_pages_median,isbn,cover_i" {
					t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
				}
				if q.Has("lang") != (tc.wantLang != "") {
					t.Errorf("lang presence = %v, want %v", q.Has("lang"), tc.wantLang != "")
				}
				if ua := r.Header.Get("User-Agent"); ua != "CollectionsApp/1 (me@example.test)" {
					t.Errorf("User-Agent = %q", ua)
				}
				_, _ = w.Write(fixture(t, tc.file))
			})
			c := testClient(s, Options{Contact: "me@example.test"})
			got, err := c.Search(t.Context(), tc.query, tc.language)
			if err != nil || len(got) != tc.wantResults || count.Load() != 1 {
				t.Fatalf("results = %+v err = %v requests = %d", got, err, count.Load())
			}
			if tc.wantResults == 0 {
				return
			}
			m := got[0]
			if m.Title != tc.wantTitle || m.Authors[0] != tc.wantAuthor || m.Year != tc.wantYear || m.Pages != tc.wantPages || m.ISBN != tc.wantISBN || m.CoverID != tc.wantCover {
				t.Errorf("first result = %+v", m)
			}
			if !strings.HasPrefix(m.WorkID, "OL") || !strings.HasSuffix(m.WorkID, "W") {
				t.Errorf("work id = %q", m.WorkID)
			}
		})
	}
}

func TestSearchKeepsRankingAndOptionalFields(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture(t, "search_english.json")) })
	got, err := testClient(s, Options{}).Search(t.Context(), "the hobbit", "en-US")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if got[0].WorkID != "OL27482W" || got[1].WorkID != "OL219602W" {
		t.Errorf("ranking = %q, %q", got[0].WorkID, got[1].WorkID)
	}
	second := got[1]
	if second.Pages != 0 || second.CoverID != 0 || len(second.Authors) != 2 || second.ISBN != "9780345272577" {
		t.Errorf("sparse doc = %+v", second)
	}
}

func TestSearchSkipsMalformedDocs(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"docs":[{"key":"/authors/OL1A","title":"x"},{"key":"/works/OL1W","title":"  "},{"key":"/works/../x","title":"y"},{"key":"/works/OL2W","title":"Kept","isbn":["bad","12345"]}]}`))
	})
	got, err := testClient(s, Options{}).Search(t.Context(), "x", "")
	if err != nil || len(got) != 1 || got[0].WorkID != "OL2W" || got[0].ISBN != "" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestWorkRequeriesByKey(t *testing.T) {
	s, count := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("q") != "key:/works/OL27482W" || q.Get("limit") != "1" || q.Get("lang") != "it" {
			t.Errorf("request = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(fixture(t, "work_hobbit.json"))
	})
	m, err := testClient(s, Options{}).Work(t.Context(), "OL27482W", "it-IT")
	if err != nil || m.WorkID != "OL27482W" || m.Title != "The Hobbit" || m.ISBN != "9780261103283" || count.Load() != 1 {
		t.Fatalf("work = %+v err = %v", m, err)
	}
}

func TestWorkRejectsBadIDsBeforeAnyRequest(t *testing.T) {
	s, count := fakeServer(t, func(http.ResponseWriter, *http.Request) {})
	c := testClient(s, Options{})
	for _, id := range []string{"", "OL1", "ol1w", "OL1W/../x", "OL1W?x=1", "OL1W ", "/works/OL1W", "OLW", "OL99999999999999999999999999999999999999999999999999999999999999999X"} {
		if _, err := c.Work(t.Context(), id, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("Work(%q) error = %v, want ErrInvalid", id, err)
		}
	}
	if count.Load() != 0 {
		t.Errorf("%d requests reached the server", count.Load())
	}
}

func TestWorkNotFoundIsAnError(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture(t, "search_misspelling.json")) })
	if _, err := testClient(s, Options{}).Work(t.Context(), "OL1W", ""); err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrDisabled) {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchIsCached(t *testing.T) {
	s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture(t, "search_english.json")) })
	c := testClient(s, Options{})
	for range 3 {
		if _, err := c.Search(t.Context(), "The Hobbit", "en-US"); err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Errorf("requests = %d, want 1 (cached)", count.Load())
	}
	if _, err := c.Search(t.Context(), "The Hobbit", "it-IT"); err != nil || count.Load() != 2 {
		t.Errorf("a different language must not share the cache entry (requests = %d, err = %v)", count.Load(), err)
	}
}

func TestDisabledClient(t *testing.T) {
	s, count := fakeServer(t, func(http.ResponseWriter, *http.Request) {})
	c := testClient(s, Options{Disabled: true})
	if c.Enabled() {
		t.Fatal("disabled client is enabled")
	}
	if _, err := c.Search(t.Context(), "the hobbit", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Search error = %v", err)
	}
	if _, err := c.Work(t.Context(), "OL1W", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("Work error = %v", err)
	}
	if _, err := c.DownloadCover(t.Context(), 1); !errors.Is(err, ErrDisabled) {
		t.Errorf("DownloadCover error = %v", err)
	}
	if count.Load() != 0 {
		t.Errorf("%d requests", count.Load())
	}
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("nil client enabled")
	}
	if _, err := nilClient.Search(t.Context(), "x", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("nil client Search error = %v", err)
	}
	if !NewWithOptions(Options{}).Enabled() {
		t.Error("the default client must be enabled (no key needed)")
	}
}

// A disabled client with the fixture flag (the network-free test stack) serves the
// fixture and nothing else.
func TestDisabledClientStillServesTheFixture(t *testing.T) {
	s, count := fakeServer(t, func(http.ResponseWriter, *http.Request) {})
	c := testClient(s, Options{Disabled: true, MockFixture: true})
	if !c.Enabled() {
		t.Fatal("the fixture must keep a disabled client available")
	}
	got, err := c.Search(t.Context(), "[mock] book name", "")
	if err != nil || len(got) != 1 || got[0].WorkID != "OL900001W" {
		t.Fatalf("fixture search = %+v err = %v", got, err)
	}
	if _, err := c.Work(t.Context(), "OL900001W", ""); err != nil {
		t.Errorf("fixture work error = %v", err)
	}
	if _, err := c.DownloadCover(t.Context(), got[0].CoverID); err != nil {
		t.Errorf("fixture cover error = %v", err)
	}
	if _, err := c.Search(t.Context(), "the hobbit", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("a non-fixture query on a disabled client: error = %v", err)
	}
	if _, err := c.Work(t.Context(), "OL1W", ""); !errors.Is(err, ErrDisabled) {
		t.Errorf("a non-fixture work on a disabled client: error = %v", err)
	}
	if count.Load() != 0 {
		t.Errorf("%d requests", count.Load())
	}
}

func TestMockFixtureIsExactAndMakesNoRequests(t *testing.T) {
	s, count := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "manual" {
			t.Errorf("unexpected request %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"docs":[{"key":"/works/OL7W","title":"Manual"}]}`))
	})
	c := testClient(s, Options{MockFixture: true})
	got, err := c.Search(t.Context(), "[mock] book name", "it-IT")
	if err != nil || len(got) != 1 {
		t.Fatalf("fixture search = %+v err = %v", got, err)
	}
	m := got[0]
	if m.WorkID != "OL900001W" || m.Title != "Mock Book Title" || m.Authors[0] != "Mock Author" || m.Pages != 321 || m.Year != 2002 || m.ISBN != "9780000000002" || m.CoverID == 0 {
		t.Errorf("fixture match = %+v", m)
	}
	work, err := c.Work(t.Context(), "OL900001W", "it-IT")
	if err != nil || work.Title != m.Title {
		t.Fatalf("fixture work = %+v err = %v", work, err)
	}
	cover, err := c.DownloadCover(t.Context(), m.CoverID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(cover)); err != nil || cfg.Width == 0 {
		t.Errorf("fixture cover is not a valid image: %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("fixture made %d requests", count.Load())
	}
	// Any other query goes upstream, even with the fixture on.
	if _, err := c.Search(t.Context(), "manual", ""); err != nil || count.Load() != 1 {
		t.Errorf("manual query: err = %v requests = %d", err, count.Load())
	}
}

func TestMockFixtureOffByDefault(t *testing.T) {
	s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"docs":[]}`)) })
	got, err := testClient(s, Options{}).Search(t.Context(), "[mock] book name", "")
	if err != nil || len(got) != 0 || count.Load() != 1 {
		t.Fatalf("got %+v err %v requests %d", got, err, count.Load())
	}
}

func TestDownloadCover(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("default") != "false" || r.Header.Get("User-Agent") != "CollectionsApp/1" {
			t.Errorf("cover request = %s UA %q", r.URL.String(), r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/b/id/123-L.jpg":
			_, _ = w.Write([]byte("jpeg-bytes"))
		case "/b/id/404-L.jpg":
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c := testClient(s, Options{})
	if b, err := c.DownloadCover(t.Context(), 123); err != nil || string(b) != "jpeg-bytes" {
		t.Fatalf("cover = %q err = %v", b, err)
	}
	if _, err := c.DownloadCover(t.Context(), 404); !errors.Is(err, ErrNoCover) {
		t.Errorf("404 error = %v, want ErrNoCover", err)
	}
	if _, err := c.DownloadCover(t.Context(), 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero cover id error = %v", err)
	}
	if got := c.CoverURL(55); got != s.URL+"/b/id/55-M.jpg" {
		t.Errorf("CoverURL = %q", got)
	}
	if c.CoverURL(0) != "" {
		t.Error("CoverURL(0) must be empty")
	}
}

func TestDownloadCoverSizeBound(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte{1}, maxCoverBytes+1))
	})
	if _, err := testClient(s, Options{}).DownloadCover(t.Context(), 9); err == nil {
		t.Fatal("an oversized cover was accepted")
	}
}

func TestResponseSizeBoundAndInvalidJSON(t *testing.T) {
	for name, body := range map[string][]byte{
		"too large":    bytes.Repeat([]byte(" "), maxJSONBytes+1),
		"invalid json": []byte("<html>not json</html>"),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
			_, err := testClient(s, Options{}).Search(t.Context(), "x", "")
			if err == nil || errors.Is(err, ErrDisabled) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "html") {
				t.Errorf("error leaks the body: %v", err)
			}
		})
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var offHost atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { offHost.Add(1) }))
	t.Cleanup(other.Close)
	s, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
	c := testClient(s, Options{})
	if _, err := c.Search(t.Context(), "x", ""); err == nil {
		t.Error("a redirect was treated as success")
	}
	if _, err := c.DownloadCover(t.Context(), 5); err == nil {
		t.Error("a cover redirect was treated as success")
	}
	if offHost.Load() != 0 {
		t.Errorf("%d requests followed a redirect off the host", offHost.Load())
	}
}

func TestUpstreamFailureDoesNotLeakDetails(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("private upstream diagnostic"))
	})
	_, err := testClient(s, Options{}).Search(t.Context(), "x", "")
	if err == nil || strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("error = %v", err)
	}
}

func TestRetryAfterBlocksFurtherRequests(t *testing.T) {
	var limited atomic.Bool
	s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if !limited.Swap(true) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"docs":[]}`))
	})
	c := testClient(s, Options{})
	if _, err := c.Search(t.Context(), "first", ""); err == nil {
		t.Fatal("429 was treated as success")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "second", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second search error = %v, want a wait that outlasts the context", err)
	}
	if count.Load() != 1 {
		t.Errorf("requests = %d, want 1 (the second one must wait for Retry-After)", count.Load())
	}
}

func TestRequestsArePaced(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"docs":[]}`)) })
	c := testClient(s, Options{RequestInterval: 80 * time.Millisecond})
	start := time.Now()
	for _, q := range []string{"a", "b", "c"} {
		if _, err := c.Search(t.Context(), q, ""); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("3 requests took %s, want pacing of at least 2 intervals", elapsed)
	}
	if requestInterval < 400*time.Millisecond {
		t.Errorf("the default interval %s is not well under 3 requests/second", requestInterval)
	}
}

func TestUserAgent(t *testing.T) {
	for _, tc := range []struct{ version, contact, want string }{
		{"", "", "CollectionsApp/1"},
		{"", "me@example.test", "CollectionsApp/1 (me@example.test)"},
		{"2.1.0", "https://example.test/c", "CollectionsApp/2.1.0 (https://example.test/c)"},
		{"", "a\r\nX-Injected: 1)(", "CollectionsApp/1 (aX-Injected: 1)"},
	} {
		if got := userAgent(tc.version, tc.contact); got != tc.want {
			t.Errorf("userAgent(%q, %q) = %q, want %q", tc.version, tc.contact, got, tc.want)
		}
	}
}

func TestFixedOriginsAreUsedWhenOverridesAreInvalid(t *testing.T) {
	c := NewWithOptions(Options{APIBaseURL: "ftp://evil.test", CoverBaseURL: "https://user:pw@evil.test"})
	if c.apiBase.String() != "https://openlibrary.org" || c.coverBase.String() != "https://covers.openlibrary.org" {
		t.Errorf("origins = %s %s", c.apiBase, c.coverBase)
	}
	d := NewWithOptions(Options{})
	if d.apiBase.String() != "https://openlibrary.org" || d.coverBase.String() != "https://covers.openlibrary.org" {
		t.Errorf("production origins = %s %s", d.apiBase, d.coverBase)
	}
}

func TestSearchQuery(t *testing.T) {
	for in, want := range map[string]string{
		"9780441172719":      "isbn:9780441172719",
		"978-0-441-17271-9":  "isbn:9780441172719",
		"0-441-17271-7":      "isbn:0441172717",
		"044117271x":         "isbn:044117271X",
		"  9780441172719  ":  "isbn:9780441172719",
		"1984":               "1984",
		"isbn 9780441172719": "isbn 9780441172719",
		"Dune 9780441172719": "Dune 9780441172719",
		"the hobbit":         "the hobbit",
	} {
		if got := searchQuery(strings.TrimSpace(in)); got != want {
			t.Errorf("searchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLangOf(t *testing.T) {
	for in, want := range map[string]string{"it-IT": "it", "en_US": "en", "FR": "fr", "": "", "xyz": "", "i": "", "1a": ""} {
		if got := langOf(in); got != want {
			t.Errorf("langOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickISBN(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"0261103288", "9780261103283"}, "9780261103283"},
		{[]string{"0261103288", "950547007x"}, "0261103288"},
		{[]string{"bad", "12"}, ""},
		{nil, ""},
	} {
		if got := pickISBN(tc.in); got != tc.want {
			t.Errorf("pickISBN(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestF016_CoverRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("foreign")) }))
	t.Cleanup(other.Close)
	for _, tc := range []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		want    string
		wantErr bool
	}{
		{"chain of 3 hops on an allowed host", func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/b/id/1-L.jpg":
				http.Redirect(w, r, "/h1", http.StatusFound)
			case "/h1":
				http.Redirect(w, r, "/h2", http.StatusFound)
			case "/h2":
				http.Redirect(w, r, "/h3", http.StatusFound)
			default:
				_, _ = w.Write([]byte("image-bytes"))
			}
		}, "image-bytes", false},
		{"4 hops are refused", func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/b/id/1-L.jpg":
				http.Redirect(w, r, "/h1", http.StatusFound)
			case "/h1":
				http.Redirect(w, r, "/h2", http.StatusFound)
			case "/h2":
				http.Redirect(w, r, "/h3", http.StatusFound)
			case "/h3":
				http.Redirect(w, r, "/h4", http.StatusFound)
			default:
				_, _ = w.Write([]byte("image-bytes"))
			}
		}, "", true},
		{"foreign host is refused", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fakeServer(t, tc.handler)
			b, err := testClient(s, Options{}).DownloadCover(t.Context(), 1)
			if (err != nil) != tc.wantErr || string(b) != tc.want {
				t.Fatalf("cover = %q err = %v", b, err)
			}
		})
	}
}

func TestF016_AllowedCoverRedirect(t *testing.T) {
	c := NewWithOptions(Options{})
	for raw, want := range map[string]bool{
		"https://archive.org/download/x.zip/1-L.jpg":           true,
		"https://ia800000.us.archive.org/view_archive.php?a=b": true,
		"https://covers.openlibrary.org/b/id/1-L.jpg":          true,
		"https://openlibrary.org/x":                            true,
		"http://archive.org/x":                                 false,
		"https://evilarchive.org/x":                            false,
		"https://archive.org.evil.test/x":                      false,
		"https://user@archive.org/x":                           false,
		"https://169.254.169.254/latest":                       false,
		"https://example.com/x":                                false,
	} {
		u, _ := url.Parse(raw)
		if got := c.allowedCoverRedirect(u); got != want {
			t.Errorf("allowedCoverRedirect(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestF016_CoverStatusMapping(t *testing.T) {
	s, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := testClient(s, Options{}).DownloadCover(t.Context(), 3); !errors.Is(err, ErrNoCover) {
		t.Fatalf("error = %v, want ErrNoCover", err)
	}
}

func TestF016_RetryOnTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int // per request; the last one repeats
		wantReqs int32
		wantErr  bool
	}{
		{"503 then success", []int{503, 200}, 2, false},
		{"persistent 503 retries once only", []int{503}, 2, true},
		{"404 is not retried", []int{404}, 1, true},
		{"429 is not retried", []int{429}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
				i := int(n.Add(1)) - 1
				if i >= len(tc.statuses) {
					i = len(tc.statuses) - 1
				}
				if tc.statuses[i] != 200 {
					w.WriteHeader(tc.statuses[i])
					return
				}
				_, _ = w.Write(fixture(t, "search_english.json"))
			})
			_, err := testClient(s, Options{}).Search(t.Context(), "x", "")
			if (err != nil) != tc.wantErr || count.Load() != tc.wantReqs {
				t.Fatalf("err = %v, requests = %d, want %d", err, count.Load(), tc.wantReqs)
			}
		})
	}
}

func TestF016_NoRetryAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := testClient(s, Options{}).Search(ctx, "x", ""); err == nil {
		t.Fatal("expected an error")
	}
	if count.Load() != 1 {
		t.Errorf("requests = %d, want 1", count.Load())
	}
}

func TestF016_WorkServedFromSearchCache(t *testing.T) {
	s, count := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture(t, "search_english.json")) })
	c := testClient(s, Options{})
	results, err := c.Search(t.Context(), "the hobbit", "en-US")
	if err != nil || len(results) == 0 {
		t.Fatalf("search = %v err = %v", results, err)
	}
	m, err := c.Work(t.Context(), results[0].WorkID, "en-US")
	if err != nil || m.WorkID != results[0].WorkID || m.Title != results[0].Title || m.CoverID != results[0].CoverID || count.Load() != 1 {
		t.Fatalf("work = %+v err = %v requests = %d", m, err, count.Load())
	}
	// Another language, or an unseen work, still asks upstream.
	if _, err := c.Work(t.Context(), results[0].WorkID, "it-IT"); err != nil || count.Load() != 2 {
		t.Errorf("requests = %d, want 2", count.Load())
	}
}
