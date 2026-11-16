package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// F-028 B3 (D-163) over HTTP: sort=rating, the genre filter, the genres of a list and the list sort
// mode. Genres and scores are written as the lookup writes them (clients cannot send them).

// setRated writes the imported score and genres of an item, as the lookup does.
func (e *treeEnv) setRated(it obj, rating any, genres ...string) {
	e.t.Helper()
	meta := obj{}
	if rating != nil {
		meta["tmdb_rating"] = rating
	}
	if genres != nil {
		meta["genres"] = genres
	}
	b, _ := json.Marshal(meta)
	e.exec(`UPDATE items SET metadata = metadata || $2::jsonb WHERE id = $1`, it["id"], string(b))
}

func TestF028B3_HTTPRatingSortGenreFilterAndGenres(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	high := e.semItem(films, "High", nil)
	low := e.semItem(films, "Low", nil)
	none := e.semItem(films, "None", nil)
	tie := e.semItem(films, "Tie", nil)
	e.setRated(high, 8.4, "Crime", "Drama")
	e.setRated(low, 5.1, "science fiction")
	e.setRated(tie, 8.4, " crime ")
	_ = none

	// sort=rating: highest first, the tie by id, the unrated item last.
	rows, page := e.page(films, semQuery("sort", "rating"))
	ties := tied(high, tie)
	inOrderIDs(t, rows, join(ties, []string{low["id"].(string), none["id"].(string)}))
	if page["total"] != float64(4) {
		t.Errorf("total = %v", page["total"])
	}

	// genre: any-of, case-insensitive; the filter is ANDed with q and paged after.
	rows, page = e.page(films, "?"+url.Values{"genre": {"CRIME", "science fiction"}}.Encode())
	sameTitles(t, rows, "High", "Tie", "Low")
	if page["total"] != float64(3) {
		t.Errorf("genre total = %v, want 3", page["total"])
	}
	rows, page = e.page(films, "?"+url.Values{"genre": {"crime"}, "q": {"tie"}}.Encode())
	sameTitles(t, rows, "Tie")
	if page["total"] != float64(1) {
		t.Errorf("genre and q total = %v, want 1", page["total"])
	}
	// Paging cuts the filtered, sorted rows: offset 1 is the second crime item by rating.
	rows, page = e.page(films, "?"+url.Values{"genre": {"crime"}, "sort": {"rating"}, "limit": {"1"}, "offset": {"1"}}.Encode())
	inOrderIDs(t, rows, ties[1:2])
	if page["total"] != float64(2) {
		t.Errorf("paged genre total = %v, want 2", page["total"])
	}

	// genres: counts, sorted by name, first spelling, [] for another type.
	rec := e.call("GET", "/api/v1/lists/"+films+"/genres", "", "")
	wantStatus(t, rec, http.StatusOK)
	var got struct {
		Genres []obj `json:"genres"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 3 || got.Genres[0]["name"] != "Crime" || got.Genres[0]["count"] != float64(2) ||
		got.Genres[1]["name"] != "Drama" || got.Genres[2]["name"] != "science fiction" {
		t.Errorf("genres = %v", got.Genres)
	}

	books := e.listOfType("Books", nil, "book")["id"].(string)
	e.semItem(books, "Dune", nil)
	rec = e.call("GET", "/api/v1/lists/"+books+"/genres", "", "")
	wantStatus(t, rec, http.StatusOK)
	if body := strings.TrimSpace(rec.Body.String()); body != `{"genres":[]}` {
		t.Errorf("book genres body = %s, want an empty array", body)
	}

	// A list that does not exist is a 404.
	wantProblem(t, e.call("GET", "/api/v1/lists/00000000-0000-0000-0000-000000000000/genres", "", ""), 404, "not_found")
}

func TestF028B3_HTTPRejections(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	books := e.listOfType("Books", nil, "book")["id"].(string)
	e.semItem(books, "Dune", nil)
	for name, tc := range map[string]struct {
		list, query, code string
	}{
		"rating sort on a book list":  {books, "?sort=rating", "invalid_sort"},
		"genre filter on a book list": {books, "?genre=Drama", "invalid_filter"},
		"empty genre value":           {films, "?genre=", "invalid_filter"},
		"eleven genre values":         {films, "?" + url.Values{"genre": repeat("Drama", 11)}.Encode(), "invalid_filter"},
	} {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, e.call("GET", "/api/v1/lists/"+tc.list+"/items"+tc.query, "", ""), 422, tc.code)
		})
	}
	// The score sort is a sort mode of movie and TV lists only.
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"Plain","type":"book","sort_mode":"rating"}`, ""), 422, "validation_failed")
	wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"Ranked","type":"movie","sort_mode":"rating"}`, ""), http.StatusCreated)
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
