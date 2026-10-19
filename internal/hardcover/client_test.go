package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "tok-SECRET-123"

func newTestClient(t *testing.T, handler http.HandlerFunc, mutate func(*Options)) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	o := Options{Token: testToken, BaseURL: s.URL, ImageOrigin: s.URL, HTTPClient: s.Client(), RequestInterval: time.Millisecond}
	if mutate != nil {
		mutate(&o)
	}
	return NewWithOptions(o)
}

func resultsBody(docs ...string) string {
	hits := make([]string, len(docs))
	for i, d := range docs {
		hits[i] = `{"document":` + d + `}`
	}
	return `{"data":{"search":{"results":{"hits":[` + strings.Join(hits, ",") + `]}}}}`
}

func TestSearchRequestShape(t *testing.T) {
	var got struct {
		Query     string
		Variables map[string]any
	}
	var auth, ct, ua string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth, ct, ua = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("User-Agent")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(resultsBody()))
	}, func(o *Options) { o.Token = ` "Bearer ` + testToken + `" `; o.Contact = "me@example.test" })
	if _, _, err := c.FindByISBN(t.Context(), "978-0-441-17271-9"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+testToken || ct != "application/json" || ua != "CollectionsApp/1 (me@example.test)" {
		t.Errorf("headers: auth=%q ct=%q ua=%q", auth, ct, ua)
	}
	// Bare digits, no isbn: prefix; per_page 3 for ISBN searches.
	if got.Variables["q"] != "9780441172719" || got.Variables["n"] != float64(3) || !strings.Contains(got.Query, `query_type: "Book"`) {
		t.Errorf("request = %+v", got)
	}
}

func TestFindByISBN(t *testing.T) {
	doc := func(isbns string) string {
		return `{"title":"Dune","author_names":["Frank Herbert","  "],"pages":608,"isbns":` + isbns + `,"image":{"url":"https://assets.hardcover.app/x.jpg"}}`
	}
	for _, tc := range []struct {
		name, isbn, body string
		ok               bool
	}{
		{"exact", "9780441172719", resultsBody(doc(`["9780441172719"]`)), true},
		{"hyphenated hit", "9780441172719", resultsBody(doc(`["978-0-441-17271-9"]`)), true},
		{"isbn10 counterpart", "9780441172719", resultsBody(doc(`["0441172717"]`)), true},
		{"isbn13 counterpart", "0441172717", resultsBody(doc(`["9780441172719"]`)), true},
		{"not in hit", "9780441172719", resultsBody(doc(`["9781111111111"]`)), false},
		{"no isbns", "9780441172719", resultsBody(doc(`null`)), false},
		{"second hit", "9780441172719", resultsBody(doc(`["9781111111111"]`), doc(`["9780441172719"]`)), true},
		{"no hits", "9780441172719", resultsBody(), false},
		{"results as string", "9780441172719", `{"data":{"search":{"results":"{\"hits\":[{\"document\":{\"title\":\"Dune\",\"isbns\":[\"9780441172719\"]}}]}"}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }, nil)
			b, ok, err := c.FindByISBN(t.Context(), tc.isbn)
			if err != nil || ok != tc.ok {
				t.Fatalf("ok=%v err=%v, want ok=%v", ok, err, tc.ok)
			}
			if ok && tc.name == "exact" && (b.Pages != 608 || len(b.Authors) != 1 || b.Authors[0] != "Frank Herbert" || b.ImageURL != "https://assets.hardcover.app/x.jpg") {
				t.Errorf("book = %+v", b)
			}
		})
	}
}

func TestSearchTitleParsesDocument(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(resultsBody(`{"title":"Dune","alternative_titles":["Dune 1"," "],"author_names":["F"],"pages":null,"isbns":["x","9780441172719","0441172717"],"image":null}`)))
	}, nil)
	books, err := c.SearchTitle(t.Context(), " Dune ")
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	b := books[0]
	if b.Pages != 0 || b.ImageURL != "" || len(b.AlternativeTitles) != 1 || b.FirstISBN() != "9780441172719" {
		t.Errorf("book = %+v isbn=%q", b, b.FirstISBN())
	}
}

func TestFailuresAreErrorsWithoutTheToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		mutate  func(*Options)
	}{
		{"401", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }, nil},
		{"403", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }, nil},
		{"429", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }, nil},
		{"500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, nil},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{\"data\": \"\x01")) }, nil},
		{"graphql error", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Bearer ` + testToken + `"}]}`))
		}, nil},
		{"too large", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", maxJSONBytes+10)))
		}, nil},
		{"timeout", func(_ http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }, func(o *Options) { o.Timeout = 20 * time.Millisecond }},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, tc.handler, tc.mutate)
			_, err := c.SearchTitle(t.Context(), "Dune")
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestNetworkErrorDoesNotLeakTheToken(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close() // connection refused
	c := NewWithOptions(Options{Token: testToken, BaseURL: url, RequestInterval: time.Millisecond})
	_, err := c.SearchTitle(t.Context(), "Dune")
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), url) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetriesOnceOn5xxThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(resultsBody(`{"title":"Dune"}`)))
	}, nil)
	books, err := c.SearchTitle(t.Context(), "Dune")
	if err != nil || len(books) != 1 || calls.Load() != 2 {
		t.Fatalf("books=%v err=%v calls=%d", books, err, calls.Load())
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(401) }, nil)
	if _, err := c.SearchTitle(t.Context(), "Dune"); err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestCancelledContextIsReturnedAsIs(t *testing.T) {
	c := newTestClient(t, func(_ http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.SearchTitle(ctx, "Dune"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabledClientNeverCalls(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, func(o *Options) { o.Token = " \"\" " })
	if c.Enabled() {
		t.Fatal("blank token must disable")
	}
	if _, err := c.SearchTitle(t.Context(), "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := c.FindByISBN(t.Context(), "9780441172719"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.DownloadCover(t.Context(), "https://assets.hardcover.app/x.jpg"); err != ErrDisabled {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 0 || (*Client)(nil).Enabled() || New("", "").Enabled() {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestPacingSpacesRequests(t *testing.T) {
	var times []time.Time
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		times = append(times, time.Now())
		_, _ = w.Write([]byte(resultsBody()))
	}, func(o *Options) { o.RequestInterval = 80 * time.Millisecond })
	for range 2 {
		if _, err := c.SearchTitle(t.Context(), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if gap := times[1].Sub(times[0]); gap < 70*time.Millisecond {
		t.Errorf("gap = %v", gap)
	}
}

func TestDownloadCover(t *testing.T) {
	var sawAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/ok.jpg":
			_, _ = w.Write([]byte("IMG"))
		case "/redir.jpg":
			http.Redirect(w, r, "/ok.jpg", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}, nil)
	base := c.imageOrigin.String()
	if b, err := c.DownloadCover(t.Context(), base+"/ok.jpg"); err != nil || string(b) != "IMG" {
		t.Fatalf("b=%q err=%v", b, err)
	}
	if sawAuth != "" {
		t.Errorf("token sent to the image host: %q", sawAuth)
	}
	for _, bad := range []string{
		"https://evil.example/ok.jpg", "http://assets.hardcover.app/x.jpg", "//assets.hardcover.app/x.jpg",
		"https://user@" + c.imageOrigin.Host + "/ok.jpg", "", "ftp://x", base + "/redir.jpg", base + "/missing.jpg",
	} {
		if _, err := c.DownloadCover(t.Context(), bad); err == nil {
			t.Errorf("DownloadCover(%q) should fail", bad)
		}
	}
}

func TestProductionImageOriginIsHardcoverAssets(t *testing.T) {
	c := New("tok", "")
	if c.imageOrigin.String() != "https://assets.hardcover.app" || c.endpoint != "https://api.hardcover.app/v1/graphql" {
		t.Fatalf("origin=%v endpoint=%v", c.imageOrigin, c.endpoint)
	}
}

func TestISBNHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"978-0-441-17271-9": "9780441172719", "0-306-40615-2": "0306406152", "080442957x": "080442957X",
		"12345": "", "": "", "isbn: 9780441172719": "9780441172719",
	} {
		if got := CleanISBN(in); got != want {
			t.Errorf("CleanISBN(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"9780441172719": "0441172717", "0441172717": "9780441172719", "080442957X": "9780804429573",
		"9791032305690": "", "9780804429573": "080442957X",
	} {
		if got := counterpartISBN(in); got != want {
			t.Errorf("counterpartISBN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanToken(t *testing.T) {
	for in, want := range map[string]string{
		"abc": "abc", ` "abc" `: "abc", "Bearer abc": "abc", `"Bearer abc"`: "abc", "bearer  abc": "abc", "": "", "Bearer ": "",
	} {
		if got := cleanToken(in); got != want {
			t.Errorf("cleanToken(%q) = %q, want %q", in, got, want)
		}
	}
}
