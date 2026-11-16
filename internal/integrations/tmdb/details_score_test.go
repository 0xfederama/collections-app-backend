package tmdb

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// F-028 (D-162): the summary, the TMDB score and the genres of a movie or TV details call.

func TestF028B1_DetailsReadsSummaryScoreVotesAndGenres(t *testing.T) {
	c, count := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"title":"Example","overview":"  A plot.  ","vote_average":7.36,"vote_count":1200,` +
			`"genres":[{"id":18,"name":"Drama"},{"id":1,"name":" "},{"id":878,"name":" Science Fiction "}]}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "it-IT", "")
	if err != nil {
		t.Fatal(err)
	}
	if *count != 1 {
		t.Errorf("calls = %d, want 1 (the overview is in the metadata language)", *count)
	}
	if d.Overview != "A plot." {
		t.Errorf("overview = %q", d.Overview)
	}
	if d.Metadata["tmdb_vote_count"] != 1200 || d.Metadata["tmdb_rating"] != 7.4 {
		t.Errorf("score = %v votes = %v, want 7.4 and 1200", d.Metadata["tmdb_rating"], d.Metadata["tmdb_vote_count"])
	}
	if want := []string{"Drama", "Science Fiction"}; !reflect.DeepEqual(d.Metadata["genres"], want) {
		t.Errorf("genres = %#v, want %#v", d.Metadata["genres"], want)
	}
}

func TestF028B1_RatingNeedsTenVotes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		votes      int
		wantRating bool
	}{
		{"nine votes: no score", 9, false},
		{"ten votes: score", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":42,"title":"Example","vote_average":8.04,"vote_count":` + strconv.Itoa(tc.votes) + `}`))
			})
			d, err := c.Details(context.Background(), "movie", 42, "en-US", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := d.Metadata["tmdb_rating"]; ok != tc.wantRating {
				t.Errorf("tmdb_rating present = %v, want %v (metadata %#v)", ok, tc.wantRating, d.Metadata)
			}
			// The count is always imported, low or not.
			if d.Metadata["tmdb_vote_count"] != tc.votes {
				t.Errorf("tmdb_vote_count = %v, want %d", d.Metadata["tmdb_vote_count"], tc.votes)
			}
		})
	}
}

func TestF028B1_NoGenresOmitsTheKey(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"title":"Example","vote_count":0,"genres":[]}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Metadata["genres"]; ok {
		t.Errorf("genres = %#v, want no key", d.Metadata["genres"])
	}
}

func TestF028B1_GenresAreCappedAtTen(t *testing.T) {
	var parts []string
	for i := 0; i < 12; i++ {
		parts = append(parts, `{"name":"Genre `+strconv.Itoa(i)+`"}`)
	}
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"title":"Example","genres":[` + strings.Join(parts, ",") + `]}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Metadata["genres"].([]string)
	if len(got) != 10 || got[0] != "Genre 0" || got[9] != "Genre 9" {
		t.Errorf("genres = %#v, want the first ten in order", got)
	}
}

func TestF028B1_TVShowReadsTheSameFields(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/4" {
			t.Errorf("TV details path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":4,"name":"Series","overview":"A series.","vote_average":8.2,"vote_count":300,"genres":[{"name":"Crime"}]}`))
	})
	d, err := c.Details(context.Background(), "tv_show", 4, "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Overview != "A series." || d.Metadata["tmdb_rating"] != 8.2 || d.Metadata["tmdb_vote_count"] != 300 {
		t.Errorf("details = %+v", d)
	}
	if want := []string{"Crime"}; !reflect.DeepEqual(d.Metadata["genres"], want) {
		t.Errorf("genres = %#v", d.Metadata["genres"])
	}
}

// TestF028B1_EnglishOverviewFallback: an empty overview in a non-English language triggers one extra
// en-US call, for the overview only; the metadata still comes from the requested language.
func TestF028B1_EnglishOverviewFallback(t *testing.T) {
	var languages []string
	c, count := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		languages = append(languages, r.URL.Query().Get("language"))
		if r.URL.Query().Get("language") == "en-US" {
			_, _ = w.Write([]byte(`{"overview":"An English plot."}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":42,"title":"Titolo","overview":"","vote_count":50,"vote_average":6.0,"genres":[{"name":"Dramma"}]}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "it-IT", "")
	if err != nil {
		t.Fatal(err)
	}
	if *count != 2 || !reflect.DeepEqual(languages, []string{"it-IT", "en-US"}) {
		t.Fatalf("calls = %d languages = %v, want one it-IT and one en-US call", *count, languages)
	}
	if d.Overview != "An English plot." || d.Title != "Titolo" || d.Metadata["genres"].([]string)[0] != "Dramma" {
		t.Errorf("details = %+v", d)
	}
}

func TestF028B1_EnglishFallbackOnlyForNonEnglish(t *testing.T) {
	c, count := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"title":"Example","overview":""}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "en-GB", "")
	if err != nil {
		t.Fatal(err)
	}
	if *count != 1 || d.Overview != "" {
		t.Errorf("en-GB calls = %d overview = %q, want one call and no summary", *count, d.Overview)
	}
}

// A failed English fallback is best effort: the title is still imported, without a summary.
func TestF028B1_EnglishFallbackFailureKeepsTheImport(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("language") == "en-US" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":42,"title":"Titolo","overview":""}`))
	})
	d, err := c.Details(context.Background(), "movie", 42, "it-IT", "")
	if err != nil {
		t.Fatalf("details failed: %v", err)
	}
	if d.Title != "Titolo" || d.Overview != "" {
		t.Errorf("details = %+v", d)
	}
}

func TestF028B1_MockFixtureCarriesSummaryScoreAndGenres(t *testing.T) {
	c := NewWithOptions("", Options{MockFixture: true, APIBaseURL: "https://127.0.0.1:1/3"})
	d, err := c.Details(context.Background(), "movie", mockMovieID, "it-IT", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Overview != mockOverview || d.Metadata["tmdb_rating"] != mockRating || d.Metadata["tmdb_vote_count"] != mockVotes {
		t.Errorf("mock details = %+v", d)
	}
	if !reflect.DeepEqual(d.Metadata["genres"], []string{"Drama", "Science Fiction"}) {
		t.Errorf("mock genres = %#v", d.Metadata["genres"])
	}
}
