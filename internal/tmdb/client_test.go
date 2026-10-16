package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *int) {
	t.Helper()
	count := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; handler(w, r) }))
	t.Cleanup(s.Close)
	return NewWithOptions("test-secret", Options{APIBaseURL: s.URL + "/3", ImageBaseURL: s.URL, HTTPClient: s.Client()}), &count
}

func TestDisabledClient(t *testing.T) {
	c := New("")
	if c.Enabled() {
		t.Fatal("empty key enabled client")
	}
	if _, err := c.Search(context.Background(), "movie", "Alien", "en-US"); err != ErrDisabled {
		t.Fatalf("Search error = %v", err)
	}
}

func TestSearchAuthLocalizationRankingAndCache(t *testing.T) {
	c, calls := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/search/movie" || r.URL.Query().Get("language") != "it-IT" || r.URL.Query().Get("query") != "odissea" {
			t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("api_key") != "test-secret" {
			t.Errorf("missing API key query parameter")
		}
		_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"2001: Odissea nello spazio","original_title":"2001: A Space Odyssey","release_date":"1968-04-02","poster_path":"/poster.jpg","overview":"Uno spazio."},{"id":2,"title":"Odissea","original_title":"Odyssey","release_date":"1997-01-01"}]}`))
	})
	a, err := c.Search(context.Background(), "movie", "odissea", "it-IT")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Search(context.Background(), "movie", "odissea", "it-IT")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || len(a) != 2 || a[0].ID != 1 || a[1].ID != 2 || a[0].OriginalTitle != "2001: A Space Odyssey" || a[0].Year != 1968 || !strings.Contains(a[0].PosterURL, "/w185/") {
		t.Fatalf("results=%+v calls=%d", a, *calls)
	}
	a[0].Title = "mutated"
	if b[0].Title == "mutated" {
		t.Fatal("cache result was not copied")
	}
}

func TestDetailsMetadataAndProviders(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/42" || !strings.Contains(r.URL.Query().Get("append_to_response"), "credits") {
			t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"id":42,"title":"Example","release_date":"2001-01-01","runtime":123,"poster_path":"/poster.jpg","credits":{"crew":[{"job":"Director","name":"One"},{"job":"Director","name":"Two"}]},"external_ids":{"imdb_id":"tt1234567"},"watch/providers":{"results":{"IT":{"link":"https://www.justwatch.com/it/movie/example","flatrate":[{"provider_name":"Prime Video","provider_id":1}],"free":[{"provider_name":"Pluto TV","provider_id":2}],"ads":[{"provider_name":"Pluto TV","provider_id":2}]}}}}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "it-IT", "IT")
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Example" || d.Metadata["release_year"] != 2001 || d.Metadata["runtime_minutes"] != 123 || d.Metadata["director"] != "One, Two" || d.Metadata["imdb_url"] != "https://www.imdb.com/title/tt1234567/" || d.WatchURL == "" {
		t.Fatalf("details = %+v", d)
	}
	streaming := d.Metadata["streaming"].(map[string]any)
	if strings.Join(streaming["platforms"].([]string), ",") != "Prime Video,Pluto TV" || strings.Join(streaming["paid_platforms"].([]string), ",") != "Prime Video" {
		t.Fatalf("streaming = %#v", streaming)
	}
}

func TestDetailsTVMetadata(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/4" {
			t.Errorf("TV details path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":4,"name":"Series","first_air_date":"2010-01-01","last_air_date":"2018-01-01","number_of_seasons":5,"episode_run_time":[44],"status":"Returning Series"}`))
	})
	d, err := c.Details(context.Background(), "tv_show", 4, "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 4 || d.Metadata["first_air_year"] != 2010 || d.Metadata["end_year"] != nil || d.Metadata["seasons"] != 5 || d.Metadata["episode_minutes"] != 44 {
		t.Fatalf("metadata = %#v", d.Metadata)
	}
}

func TestTVEndedYearAndDetailsIDValidation(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/4" {
			t.Errorf("TV details path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":4,"name":"Series","first_air_date":"2010-01-01","last_air_date":"2018-01-01","status":"Ended"}`))
	})
	d, err := c.Details(context.Background(), "tv_show", 4, "en-US", "")
	if err != nil || d.Metadata["end_year"] != 2018 {
		t.Fatalf("ended details = %+v, %v", d, err)
	}

	wrongID, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":9,"title":"Wrong result"}`))
	})
	if _, err := wrongID.Details(context.Background(), "movie", 4, "en-US", ""); err == nil {
		t.Fatal("accepted mismatched upstream details ID")
	}
	missingID, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"title":"No ID"}`))
	})
	if _, err := missingID.Details(context.Background(), "movie", 4, "en-US", ""); err == nil {
		t.Fatal("accepted missing upstream details ID")
	}
}

func TestRateLimitTimeoutBoundsAndUnsafePoster(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/movie":
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/3/search/tv":
			_, _ = w.Write([]byte(strings.Repeat("x", maxJSONBytes+1)))
		case "/w500/poster.jpg":
			_, _ = w.Write([]byte(strings.Repeat("x", maxPosterBytes+1)))
		default:
			http.NotFound(w, r)
		}
	})
	if _, err := c.Search(context.Background(), "movie", "x", "en-US"); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("429 error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "tv_show", "x", "en-US"); err == nil {
		t.Fatal("rate wait ignored context cancellation")
	}
	// Expire the test throttle to exercise bounded response handling without waiting.
	c.mu.Lock()
	c.blockedUntil = time.Time{}
	c.nextRequest = time.Time{}
	c.mu.Unlock()
	if _, err := c.Search(context.Background(), "tv_show", "x", "en-US"); err == nil {
		t.Fatal("accepted oversized JSON")
	}
	for _, p := range []string{"//evil.test/a.jpg", "/../secret.jpg", "/a/../b.jpg", "/a\\b.jpg", "https://evil.test/a.jpg"} {
		if _, err := c.DownloadPoster(context.Background(), p); err == nil {
			t.Errorf("accepted unsafe poster path %q", p)
		}
	}
	if _, err := c.DownloadPoster(context.Background(), "/poster.jpg"); err == nil {
		t.Fatal("accepted oversized poster")
	}
}

func TestRedirectIsBlockedAndErrorsDoNotLeakKey(t *testing.T) {
	var targetCalls int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	_, err := c.Search(context.Background(), "movie", "x", "en-US")
	if err == nil || strings.Contains(err.Error(), "test-secret") || targetCalls != 0 {
		t.Fatalf("error=%v targetCalls=%d", err, targetCalls)
	}
}

func TestHTTPTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer s.Close()
	c := NewWithOptions("test-secret", Options{APIBaseURL: s.URL + "/3", ImageBaseURL: s.URL, HTTPClient: s.Client(), Timeout: 15 * time.Millisecond})
	if _, err := c.Search(context.Background(), "movie", "slow", "en-US"); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestErrorsHideKeyAndRetryAfterIsBounded(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "9223372036854775807")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("upstream diagnostic contains test-secret"))
	})
	_, err := c.Search(context.Background(), "movie", "x", "en-US")
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("upstream error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "movie", "again", "en-US"); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("Retry-After wait error = %v", err)
	}
}

func TestMalformedBaseURLDisablesClient(t *testing.T) {
	c := NewWithOptions("test-secret", Options{APIBaseURL: "://bad", ImageBaseURL: "https://images.example.test"})
	if c.Enabled() {
		t.Fatal("malformed endpoint left client enabled")
	}
}

func TestRequestTimeoutIncludesRateLimitWait(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s.Close()
	c := NewWithOptions("test-secret", Options{APIBaseURL: s.URL + "/3", ImageBaseURL: s.URL, HTTPClient: s.Client(), Timeout: 20 * time.Millisecond})
	if _, err := c.Search(context.Background(), "movie", "first", "en-US"); err == nil {
		t.Fatal("expected rate limit response")
	}
	start := time.Now()
	if _, err := c.Search(context.Background(), "movie", "queued", "en-US"); err == nil {
		t.Fatal("queued request ignored client timeout")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("queued request took too long: %s", time.Since(start))
	}
}
