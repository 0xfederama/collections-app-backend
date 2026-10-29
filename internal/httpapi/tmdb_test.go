package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func tmdbRouter(t *testing.T, client *tmdb.Client) http.Handler {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	workspaceID, err := workspace.Bootstrap(t.Context(), pool, "W")
	if err != nil {
		t.Fatal(err)
	}
	return newRouter(t, httpapi.Deps{
		DB: pool, DataDir: t.TempDir(), Logger: quietLogger(), WorkspaceID: workspaceID,
		Settings: workspace.NewService(pool), TMDB: lookup.NewTMDBProvider(client), LookupAvailable: client.Enabled(),
	})
}

func TestF013B2_SuggestionsDisabledWithoutKey(t *testing.T) {
	h := newRouter(t, httpapi.Deps{
		DB: fakePinger{}, DataDir: t.TempDir(), Logger: quietLogger(), TMDB: lookup.NewTMDBProvider(tmdb.New("")),
	})
	rec := do(t, h, req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=movie&q=Alien"})
	wantStatus(t, rec, http.StatusOK)
	got := decode(t, rec)
	if got["available"] != false || len(got["results"].([]any)) != 0 {
		t.Fatalf("disabled suggestions = %v", got)
	}
}

func TestF013B2_MockFixtureIsAvailableWithoutKeyOnlyForExactQuery(t *testing.T) {
	client := tmdb.NewWithOptions("", tmdb.Options{MockFixture: true})
	h := tmdbRouter(t, client)

	fixture := do(t, h, req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=movie&q=" + url.QueryEscape("[mock] movie name")})
	wantStatus(t, fixture, http.StatusOK)
	got := decode(t, fixture)
	results, ok := got["results"].([]any)
	if got["available"] != true || !ok || len(results) != 1 || results[0].(map[string]any)["title"] != "[mock] movie name" {
		t.Fatalf("fixture suggestions = %v", got)
	}

	other := do(t, h, req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=movie&q=Alien"})
	wantStatus(t, other, http.StatusOK)
	got = decode(t, other)
	if got["available"] != false || len(got["results"].([]any)) != 0 {
		t.Fatalf("nonfixture suggestions without key = %v", got)
	}
}

func TestF013B2_SuggestionsUseLanguageAndPreserveRanking(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/search/movie" || r.URL.Query().Get("query") != "odissea" || r.URL.Query().Get("language") != "it-IT" {
			t.Errorf("upstream request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("api_key") != "test-key" {
			t.Error("upstream request lacks API key")
		}
		_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"2001: A Space Odyssey","original_title":"2001: A Space Odyssey","release_date":"1968-04-02","poster_path":"/one.jpg","overview":"First."},{"id":2,"title":"Odyssey","original_title":"Odyssey","overview":"Second."}]}`))
	}))
	t.Cleanup(upstream.Close)
	client := tmdb.NewWithOptions("test-key", tmdb.Options{APIBaseURL: upstream.URL + "/3", ImageBaseURL: upstream.URL, HTTPClient: upstream.Client()})
	h := tmdbRouter(t, client)

	rec := do(t, h, req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=movie&q=" + url.QueryEscape("odissea")})
	wantStatus(t, rec, http.StatusOK)
	var got struct {
		Available bool `json:"available"`
		Results   []struct {
			ID        int     `json:"tmdb_id"`
			Title     string  `json:"title"`
			Year      *int    `json:"year"`
			PosterURL *string `json:"poster_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Results) != 2 || got.Results[0].ID != 1 || got.Results[1].ID != 2 {
		t.Fatalf("suggestions = %+v", got)
	}
	if got.Results[0].Year == nil || *got.Results[0].Year != 1968 || got.Results[0].PosterURL == nil || !strings.Contains(*got.Results[0].PosterURL, "/w185/one.jpg") {
		t.Fatalf("first suggestion = %+v", got.Results[0])
	}
	if got.Results[1].Year != nil || got.Results[1].PosterURL != nil {
		t.Fatalf("nullable fields = %+v", got.Results[1])
	}
}

func TestF013B2_SuggestionsSanitizeUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("private upstream diagnostic test-key"))
	}))
	t.Cleanup(upstream.Close)
	client := tmdb.NewWithOptions("test-key", tmdb.Options{APIBaseURL: upstream.URL + "/3", ImageBaseURL: upstream.URL, HTTPClient: upstream.Client()})
	rec := do(t, tmdbRouter(t, client), req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=tv_show&q=slow"})
	p := wantProblem(t, rec, http.StatusServiceUnavailable, "upstream_unavailable")
	if body := rec.Body.String(); strings.Contains(body, "test-key") || strings.Contains(body, "diagnostic") || strings.Contains(p.Detail, "502") {
		t.Fatalf("problem leaks upstream details: %s", body)
	}
}

func TestF013B2_SuggestionsRejectWhitespaceQuery(t *testing.T) {
	client := tmdb.NewWithOptions("test-key", tmdb.Options{APIBaseURL: "http://127.0.0.1", ImageBaseURL: "http://127.0.0.1"})
	rec := do(t, tmdbRouter(t, client), req{method: http.MethodGet, path: "/api/v1/tmdb/suggestions?type=movie&q=%20%20"})
	wantProblem(t, rec, http.StatusBadRequest, "invalid_parameter")
}
