package lookup

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/integrations/tmdb"
)

// F-028 (D-162): the provider adapters hand the summary, the score and the genres to the apply rules.

func TestF028B1_TMDBAdapterCarriesSummaryScoreAndGenres(t *testing.T) {
	p := NewTMDBProvider(tmdb.NewWithOptions("", tmdb.Options{MockFixture: true}))
	d, err := p.Details(t.Context(), "movie", "900001", "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Synopsis, "mock synopsis") {
		t.Errorf("synopsis = %q", d.Synopsis)
	}
	if !reflect.DeepEqual(d.Fields["genres"], []string{"Drama", "Science Fiction"}) || d.Fields["tmdb_rating"] != 7.4 || d.Fields["tmdb_vote_count"] != 1200 {
		t.Errorf("fields = %#v", d.Fields)
	}
}

func TestF028B2_OpenLibraryAdapterReadsTheWorkDescription(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/works/OL27482W.json" {
			_, _ = w.Write([]byte(`{"description":"# A hobbit goes on a journey."}`))
			return
		}
		_, _ = w.Write(openLibraryFixture(t, "work_hobbit.json"))
	}, false)
	d, err := p.Details(t.Context(), "book", "OL27482W", "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Synopsis != `\# A hobbit goes on a journey.` {
		t.Errorf("synopsis = %q, want the escaped paragraph", d.Synopsis)
	}
}

// The work record is best effort: when it fails the book is still imported, without a summary.
func TestF028B2_OpenLibraryDescriptionFailureKeepsTheBook(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/works/OL27482W.json" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(openLibraryFixture(t, "work_hobbit.json"))
	}, false)
	d, err := p.Details(t.Context(), "book", "OL27482W", "en-US", "")
	if err != nil {
		t.Fatalf("details failed: %v", err)
	}
	if d.Title != "The Hobbit" || d.Synopsis != "" || d.Fields["author"] != "J.R.R. Tolkien" {
		t.Errorf("details = %+v", d)
	}
}

// An Open Library 429 reaches the lookup as a rate limit, which stops the backfill pass (D-164).
func TestB4_OpenLibraryRateLimitIsAProviderRateLimit(t *testing.T) {
	p := openLibraryProviderFor(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}, false)
	_, err := p.Details(t.Context(), "book", "OL27482W", "en-US", "")
	if !errors.Is(err, ErrProviderRateLimited) {
		t.Fatalf("error = %v, want ErrProviderRateLimited", err)
	}
}
