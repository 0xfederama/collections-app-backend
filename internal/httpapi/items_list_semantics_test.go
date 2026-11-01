package httpapi_test

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
)

// These tests pin what GET /lists/{id}/items does today (db/queries/items.sql
// ListItems and CountItems). They are characterization tests for F-019-B3d: they
// assert the current behaviour, so the move of the list logic to Go keeps it.

// semQuery builds a query string from key, value pairs (values are escaped).
func semQuery(pairs ...string) string {
	if len(pairs) == 0 {
		return ""
	}
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return "?" + v.Encode()
}

// semItem creates an item with the given metadata (nil = no metadata).
func (e *treeEnv) semItem(listID, title string, metadata obj) obj {
	e.t.Helper()
	body := obj{"title": title}
	if metadata != nil {
		body["metadata"] = metadata
	}
	b, _ := json.Marshal(body)
	return e.newItem(listID, string(b))
}

// semSetLink writes streaming.link the way only the lookup does (clients cannot send it).
func (e *treeEnv) semSetLink(it obj, link string) {
	e.t.Helper()
	b, _ := json.Marshal(link)
	e.exec(`UPDATE items SET metadata = jsonb_set(metadata, '{streaming,link}', $2::jsonb) WHERE id = $1`, it["id"], string(b))
}

// sameTitles checks the rows hold exactly want, in any order.
func sameTitles(t *testing.T, rows []obj, want ...string) {
	t.Helper()
	got := titlesOf(rows)
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("titles = %v, want %v", got, w)
	}
}

// inOrderTitles checks the rows hold exactly want, in that order.
func inOrderTitles(t *testing.T, rows []obj, want ...string) {
	t.Helper()
	if got := titlesOf(rows); !slices.Equal(got, want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
}

// inOrderIDs checks the rows hold exactly want ids, in that order.
func inOrderIDs(t *testing.T, rows []obj, want []string) {
	t.Helper()
	if got := idsOf(rows...); !slices.Equal(got, want) {
		t.Fatalf("ids/titles = %v (%v), want %v", got, titlesOf(rows), want)
	}
}

// tied returns the ids of items that tie on the sort key, in the id order every sort ends with.
func tied(items ...obj) []string {
	ids := idsOf(items...)
	slices.Sort(ids)
	return ids
}

// join concatenates id slices into one expected order.
func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestSemanticsSearchQ(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	e.semItem(films, "The Matrix", obj{"director": "Lana Wachowski"})
	e.semItem(films, "Campion Notes", nil)
	e.semItem(films, "Zulu", obj{"director": "Jane Campion"})
	e.semItem(films, "Under 100%", obj{"director": "Sam Low"})
	e.semItem(films, "100 Years", obj{"director": "Nobody"})
	e.semItem(films, "snake_case", nil)
	e.semItem(films, "snakeXcase", nil)
	e.semItem(films, "Heat", obj{"director": "Michael Mann"})

	// A list of another type: only its titles are searched (the director is movie only).
	gizmos := e.listOfType("Gizmos", nil, "tv_show")["id"].(string)
	e.semItem(gizmos, "Gizmo", nil)

	tests := []struct {
		name, list, q string
		want          []string
	}{
		{"title, lower-case q", films, "matrix", []string{"The Matrix"}},
		{"title, upper-case q", films, "MATRIX", []string{"The Matrix"}},
		{"movie director, case-insensitive", films, "wachowski", []string{"The Matrix"}},
		{"title and director both match", films, "CAMPION", []string{"Campion Notes", "Zulu"}},
		{"percent sign is literal", films, "%", []string{"Under 100%"}},
		{"100% does not match 100 Years", films, "100%", []string{"Under 100%"}},
		{"underscore is literal", films, "_", []string{"snake_case"}},
		{"no match", films, "nothing", nil},
		{"title of a non-movie type is searched", gizmos, "GIZ", []string{"Gizmo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, page := e.page(tt.list, semQuery("q", tt.q))
			sameTitles(t, rows, tt.want...)
			if page["total"] != float64(len(tt.want)) {
				t.Fatalf("total = %v, want %d", page["total"], len(tt.want))
			}
		})
	}
}

func TestSemanticsTextAndLinkFilters(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	e.semItem(films, "Alpha", obj{"director": "Jane Campion", "imdb_url": "https://www.imdb.com/title/tt0000001/"})
	e.semItem(films, "Bravo", obj{"director": "Campbell Ray", "imdb_url": "https://www.imdb.com/title/tt1234567/"})
	e.semItem(films, "Charlie", obj{"director": "100% Real"})
	e.semItem(films, "Delta", obj{"director": "a_b Films", "imdb_url": "https://www.imdb.com/title/tt0000009/"})
	e.semItem(films, "Echo", obj{"director": "Other"})

	tests := []struct {
		name, field, value string
		want               []string
	}{
		{"text contains, lower-case", "director", "camp", []string{"Alpha", "Bravo"}},
		{"text contains, upper-case", "director", "CAMP", []string{"Alpha", "Bravo"}},
		{"text contains with a space", "director", "b films", []string{"Delta"}},
		{"text percent is literal", "director", "%", []string{"Charlie"}},
		{"text underscore is literal", "director", "_", []string{"Delta"}},
		{"text no match", "director", "nothing", nil},
		{"link contains, case-insensitive", "imdb_url", "TT0000", []string{"Alpha", "Delta"}},
		{"link contains a path", "imdb_url", "imdb.com/title/tt1234567", []string{"Bravo"}},
		{"link contains the host of every link", "imdb_url", "https://www.imdb.com", []string{"Alpha", "Bravo", "Delta"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, page := e.page(films, semQuery("sort", "title", "filter_field", tt.field, "filter_value", tt.value))
			sameTitles(t, rows, tt.want...)
			if page["total"] != float64(len(tt.want)) {
				t.Fatalf("total = %v, want %d", page["total"], len(tt.want))
			}
		})
	}
}

func TestSemanticsNumberFilter(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	e.semItem(films, "Old", obj{"release_year": 1979})
	e.semItem(films, "Nineties", obj{"release_year": 1990})
	e.semItem(films, "Millennium", obj{"release_year": 2000})
	e.semItem(films, "Recent", obj{"release_year": 2010})
	e.semItem(films, "Undated", nil)

	tests := []struct {
		name  string
		query []string
		want  []string
	}{
		{"min is inclusive and leaves out items without a value", []string{"filter_field", "release_year", "filter_min", "1990"}, []string{"Nineties", "Millennium", "Recent"}},
		{"max is inclusive", []string{"filter_field", "release_year", "filter_max", "2000"}, []string{"Old", "Nineties", "Millennium"}},
		{"min and max together are inclusive", []string{"filter_field", "release_year", "filter_min", "1990", "filter_max", "2000"}, []string{"Nineties", "Millennium"}},
		{"equal bounds match exactly", []string{"filter_field", "release_year", "filter_min", "1979", "filter_max", "1979"}, []string{"Old"}},
		{"empty range", []string{"filter_field", "release_year", "filter_min", "1991", "filter_max", "1999"}, nil},
		{"decimal bound", []string{"filter_field", "release_year", "filter_min", "1990.5"}, []string{"Millennium", "Recent"}},
		{"combined with the title search", []string{"q", "e", "filter_field", "release_year", "filter_min", "1990", "filter_max", "2000"}, []string{"Nineties", "Millennium"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, page := e.page(films, semQuery(append([]string{"sort", "title"}, tt.query...)...))
			sameTitles(t, rows, tt.want...)
			if page["total"] != float64(len(tt.want)) {
				t.Fatalf("total = %v, want %d", page["total"], len(tt.want))
			}
		})
	}

	t.Run("bad numbers and reversed bounds are 400", func(t *testing.T) {
		for _, query := range []string{
			"?filter_field=release_year&filter_min=abc",
			"?filter_field=release_year&filter_max=1e",
			"?filter_field=release_year&filter_min=2000&filter_max=1990",
		} {
			wantProblem(t, e.call("GET", "/api/v1/lists/"+films+"/items"+query, "", ""), 400, "invalid_parameter")
		}
	})
}

func TestSemanticsBooleanFilter(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	e.semItem(films, "Yes", obj{"lookup_disabled": true})
	e.semItem(films, "No", obj{"lookup_disabled": false})
	e.semItem(films, "Unknown", nil)

	t.Run("true and false select their items, unset is in neither", func(t *testing.T) {
		rows, page := e.page(films, "?filter_field=lookup_disabled&filter_value=true")
		sameTitles(t, rows, "Yes")
		if page["total"] != float64(1) {
			t.Fatalf("true total = %v", page["total"])
		}
		rows, page = e.page(films, "?filter_field=lookup_disabled&filter_value=false")
		sameTitles(t, rows, "No")
		if page["total"] != float64(1) {
			t.Fatalf("false total = %v", page["total"])
		}
	})

	t.Run("only true and false are values, and a boolean takes no range", func(t *testing.T) {
		for _, query := range []string{
			"?filter_field=lookup_disabled&filter_value=yes",
			"?filter_field=lookup_disabled&filter_value=True",
			"?filter_field=lookup_disabled&filter_value=true&filter_min=1",
		} {
			wantProblem(t, e.call("GET", "/api/v1/lists/"+films+"/items"+query, "", ""), 400, "invalid_parameter")
		}
	})
}

func TestSemanticsStreamingFilterIgnoresLink(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	alpha := e.semItem(films, "Alpha", obj{"streaming": obj{"country": "IT", "platforms": []string{"Netflix"}}})
	e.semItem(films, "Bravo", obj{"streaming": obj{"country": "DE", "platforms": []string{"Prime Video"}}})
	e.semItem(films, "Charlie", obj{"streaming": obj{"country": "US"}})
	// The lookup stores the JustWatch link inside the streaming object; the filter must not see it.
	e.semSetLink(alpha, "https://www.justwatch.com/it/movie/alpha")
	stored := e.fetchItem(alpha["id"])["metadata"].(map[string]any)["streaming"].(map[string]any)
	if stored["link"] == nil {
		t.Fatalf("setup: link not stored: %v", stored)
	}

	tests := []struct {
		name, value string
		want        []string
	}{
		{"the link is not matched", "justwatch", nil},
		{"a platform is matched", "netflix", []string{"Alpha"}},
		{"a platform with a space, case-insensitive", "PRIME video", []string{"Bravo"}},
		{"a country is matched", "us", []string{"Charlie"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, page := e.page(films, semQuery("sort", "title", "filter_field", "streaming", "filter_value", tt.value))
			sameTitles(t, rows, tt.want...)
			if page["total"] != float64(len(tt.want)) {
				t.Fatalf("total = %v, want %d", page["total"], len(tt.want))
			}
		})
	}
}

func TestSemanticsSorts(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	alpha := e.semItem(films, "Alpha", obj{"release_year": 1990, "runtime_minutes": 120, "director": "Carl",
		"lookup_disabled": true, "imdb_url": "https://www.imdb.com/title/tt0000002/", "streaming": obj{"country": "IT", "platforms": []string{"Netflix"}}})
	bravo := e.semItem(films, "Bravo", obj{"release_year": 2000, "director": "ada", "lookup_disabled": false,
		"imdb_url": "https://www.imdb.com/title/tt0000001/", "streaming": obj{"country": "DE"}})
	charlie := e.semItem(films, "Charlie", obj{"runtime_minutes": 90})
	delta := e.semItem(films, "Delta", obj{"release_year": 1990, "runtime_minutes": 90, "director": "Bea",
		"lookup_disabled": true, "streaming": obj{"country": "US"}})
	// Same streaming object as Alpha but no link: Alpha's link must not change the order.
	echo := e.semItem(films, "Echo", obj{"release_year": 1985, "streaming": obj{"country": "IT", "platforms": []string{"Netflix"}}})
	e.semSetLink(alpha, "https://www.justwatch.com/it/movie/alpha")

	tests := []struct {
		name, sort string
		want       []string
	}{
		// Numbers ascending, no value last, ties by id.
		{"number", "release_year", join([]string{echo["id"].(string)}, tied(alpha, delta), []string{bravo["id"].(string), charlie["id"].(string)})},
		{"number with null ties by id", "runtime_minutes", join(tied(charlie, delta), []string{alpha["id"].(string)}, tied(bravo, echo))},
		// Text ascending, case-insensitive, no value last.
		{"text", "director", join([]string{bravo["id"].(string), delta["id"].(string), alpha["id"].(string)}, tied(charlie, echo))},
		// Link sorts as text.
		{"link", "imdb_url", join([]string{bravo["id"].(string), alpha["id"].(string)}, tied(charlie, delta, echo))},
		// Boolean: false before true, no value last.
		{"boolean", "lookup_disabled", join([]string{bravo["id"].(string)}, tied(alpha, delta), tied(charlie, echo))},
		// Streaming sorts by the object without link (jsonb text, case-insensitive).
		{"streaming", "streaming", join([]string{bravo["id"].(string)}, tied(alpha, echo), []string{delta["id"].(string), charlie["id"].(string)})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, page := e.page(films, semQuery("sort", tt.sort))
			inOrderIDs(t, rows, tt.want)
			if page["total"] != float64(5) {
				t.Fatalf("total = %v", page["total"])
			}
		})
	}

	t.Run("updated is newest first", func(t *testing.T) {
		wantStatus(t, e.patchItem(charlie, `{"website":"https://charlie.example"}`), 200)
		rows, _ := e.page(films, "?sort=updated")
		inOrderIDs(t, rows, []string{charlie["id"].(string), echo["id"].(string), delta["id"].(string), bravo["id"].(string), alpha["id"].(string)})
	})

	t.Run("price sorts ascending, no price last, title breaks ties", func(t *testing.T) {
		wishlist := e.listOfType("Wishlist", nil, "wishlist")["id"].(string)
		priced := func(title string, min string) obj {
			b, _ := json.Marshal(obj{"title": title, "price": obj{"min": min, "currency": "EUR"}})
			return e.newItem(wishlist, string(b))
		}
		priced("b-ten", "10.00")
		priced("A-ten", "10.00")
		priced("cheap", "5.00")
		e.semItem(wishlist, "none", nil)
		rows, _ := e.page(wishlist, "?sort=price")
		inOrderTitles(t, rows, "cheap", "A-ten", "b-ten", "none")
	})
}

func TestSemanticsManualOrderWithFilter(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	m1 := e.semItem(films, "m1", obj{"lookup_disabled": true})
	m2 := e.semItem(films, "m2", obj{"lookup_disabled": false})
	m3 := e.semItem(films, "m3", obj{"lookup_disabled": true})
	wantStatus(t, e.call("PUT", "/api/v1/lists/"+films+"/items/order", idsJSON(idsOf(m3, m1, m2)...), ""), 204)

	rows, _ := e.page(films, "?sort=manual")
	inOrderTitles(t, rows, "m3", "m1", "m2")
	rows, _ = e.page(films, "?sort=manual&filter_field=lookup_disabled&filter_value=true")
	inOrderIDs(t, rows, idsOf(m3, m1))
}

func TestSemanticsLimitOffsetTotalWithFilter(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	for i, title := range []string{"p1", "p2", "p3", "p4", "p5"} {
		var meta obj
		if i < 4 {
			meta = obj{"release_year": 1990 + i}
		}
		e.semItem(films, title, meta)
	}
	filter := "filter_field=release_year&filter_min=1990"

	rows, page := e.page(films, "?sort=release_year&"+filter+"&limit=3")
	if len(rows) != 3 || page["total"] != float64(4) || page["limit"] != float64(3) || page["offset"] != float64(0) {
		t.Fatalf("page 1: %d rows, %v", len(rows), page)
	}
	inOrderTitles(t, rows, "p1", "p2", "p3")
	rows, page = e.page(films, "?sort=release_year&"+filter+"&limit=3&offset=3")
	inOrderTitles(t, rows, "p4")
	if page["total"] != float64(4) || page["offset"] != float64(3) {
		t.Fatalf("page 2: %v", page)
	}
	rows, page = e.page(films, "?sort=release_year&"+filter+"&limit=3&offset=4")
	if len(rows) != 0 || page["total"] != float64(4) {
		t.Fatalf("past the end: %v %v", titlesOf(rows), page)
	}
	rows, page = e.page(films, "?q=p&sort=title&"+filter+"&limit=2&offset=2")
	inOrderTitles(t, rows, "p3", "p4")
	if page["total"] != float64(4) {
		t.Fatalf("q with filter total = %v", page["total"])
	}
}

func TestSemanticsInvalidSortAndFilterAre400(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	books := e.listOfType("Books", nil, "book")["id"].(string)
	plain := e.list("Plain", nil)["id"].(string)
	e.semItem(plain, "x", nil)

	tests := []struct {
		name, list, query string
	}{
		{"price sort on a type without price", films, "?sort=price"},
		{"sort on a stores field", books, "?sort=stores"},
		{"filter on a stores field", books, "?filter_field=stores&filter_value=amazon"},
		{"filter value without a field", films, "?filter_value=x"},
		{"filter min without a field", films, "?filter_min=1990"},
		{"filter max without a field", films, "?filter_max=1990"},
		{"bad number", films, "?filter_field=release_year&filter_min=abc"},
		{"number field without a bound", films, "?filter_field=release_year"},
		{"text field with a range", films, "?filter_field=director&filter_min=1"},
		{"text field with a value and a range", films, "?filter_field=director&filter_value=x&filter_min=1"},
		{"boolean field with a bad value", films, "?filter_field=lookup_disabled&filter_value=maybe"},
		{"unknown filter field", films, "?filter_field=nope&filter_value=x"},
		{"filter on a list without a type", plain, "?filter_field=anything&filter_value=x"},
		{"unknown sort field", films, "?sort=nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantProblem(t, e.call("GET", "/api/v1/lists/"+tt.list+"/items"+tt.query, "", ""), 400, "invalid_parameter")
		})
	}
}
