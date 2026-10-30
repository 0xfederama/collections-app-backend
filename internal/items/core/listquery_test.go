package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// These tests are the unit form of the list characterization of F-019-B3d
// (internal/httpapi/items_list_semantics_test.go, T0) and of the list cases of
// internal/httpapi/items_test.go. Fixtures mirror the HTTP ones.

var (
	movieFields = []types.Field{
		{Key: "director", Type: "text"},
		{Key: "imdb_url", Type: "link", Special: true},
		{Key: "release_year", Type: "number"},
		{Key: "runtime_minutes", Type: "number"},
		{Key: "lookup_disabled", Type: "boolean", Special: true},
		{Key: "streaming", Type: "streaming", Special: true},
	}
	movieDefs = ListDefs{SortMode: SortManual, Fields: movieFields}
	movie     = types.Movie
	untyped   = ListDefs{SortMode: SortManual} // a list with no type: no fields, no price

	base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// mkItem builds item n: its id, sort order and update time all follow n. meta is
// the stored metadata JSON ("" = {}).
func mkItem(n int, title, meta string) Item {
	if meta == "" {
		meta = "{}"
	}
	return Item{
		ID: uuid.UUID{15: byte(n)}, Title: title, SortOrder: n,
		Metadata: json.RawMessage(meta), UpdatedAt: base.Add(time.Duration(n) * time.Second),
	}
}

func withPrice(it Item, min string) Item {
	it.Price = &Price{Min: &min, Currency: "EUR"}
	return it
}

// nums returns the ids of a page as item numbers (see mkItem), for ordered checks.
func nums(page []Item) []int {
	out := []int{}
	for _, it := range page {
		out = append(out, int(it.ID[15]))
	}
	return out
}

func titlesOf(page []Item) []string {
	out := []string{}
	for _, it := range page {
		out = append(out, it.Title)
	}
	return out
}

// sameTitles checks the page holds exactly want, in any order.
func sameTitles(t *testing.T, page []Item, want ...string) {
	t.Helper()
	got := titlesOf(page)
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("titles = %v, want %v", got, w)
	}
}

// inOrderTitles checks the page holds exactly want, in that order.
func inOrderTitles(t *testing.T, page []Item, want ...string) {
	t.Helper()
	if got := titlesOf(page); !slices.Equal(got, want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
}

func inOrderNums(t *testing.T, page []Item, want ...int) {
	t.Helper()
	if got := nums(page); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func mustQuery(t *testing.T, all []Item, typ *string, defs ListDefs, p ListParams) ([]Item, int) {
	t.Helper()
	page, total, err := Query(all, typ, defs, p)
	if err != nil {
		t.Fatalf("Query(%+v): %v", p, err)
	}
	return page, total
}

func TestQuerySearchQ(t *testing.T) {
	films := []Item{
		mkItem(1, "The Matrix", `{"director":"Lana Wachowski"}`),
		mkItem(2, "Campion Notes", ""),
		mkItem(3, "Zulu", `{"director":"Jane Campion"}`),
		mkItem(4, "Under 100%", `{"director":"Sam Low"}`),
		mkItem(5, "100 Years", `{"director":"Nobody"}`),
		mkItem(6, "snake_case", ""),
		mkItem(7, "snakeXcase", ""),
		mkItem(8, "Heat", `{"director":"Michael Mann"}`),
	}
	gadgetType := "gadget"
	gadgetDefs := ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "director", Type: "text"}}}
	gizmos := []Item{mkItem(1, "Gizmo", `{"director":"Wachowski"}`)}

	tests := []struct {
		name string
		all  []Item
		typ  *string
		defs ListDefs
		q    string
		want []string
	}{
		{"title, lower-case q", films, &movie, movieDefs, "matrix", []string{"The Matrix"}},
		{"title, upper-case q", films, &movie, movieDefs, "MATRIX", []string{"The Matrix"}},
		{"movie director, case-insensitive", films, &movie, movieDefs, "wachowski", []string{"The Matrix"}},
		{"title and director both match", films, &movie, movieDefs, "CAMPION", []string{"Campion Notes", "Zulu"}},
		{"percent sign is literal", films, &movie, movieDefs, "%", []string{"Under 100%"}},
		{"100% does not match 100 Years", films, &movie, movieDefs, "100%", []string{"Under 100%"}},
		{"underscore is literal", films, &movie, movieDefs, "_", []string{"snake_case"}},
		{"no match", films, &movie, movieDefs, "nothing", nil},
		{"director of a non-movie type is not searched", gizmos, &gadgetType, gadgetDefs, "wachowski", nil},
		{"title of a non-movie type is searched", gizmos, &gadgetType, gadgetDefs, "GIZ", []string{"Gizmo"}},
		{"backslash is literal", []Item{mkItem(1, `back\slash`, ""), mkItem(2, "other", "")}, nil, untyped, `k\s`, []string{`back\slash`}},
		{"unicode case folds", []Item{mkItem(1, "Ünïcode Ñ", ""), mkItem(2, "Other", "")}, nil, untyped, "ünï", []string{"Ünïcode Ñ"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, total := mustQuery(t, tt.all, tt.typ, tt.defs, ListParams{Q: tt.q})
			sameTitles(t, page, tt.want...)
			if total != len(tt.want) {
				t.Fatalf("total = %d, want %d", total, len(tt.want))
			}
		})
	}
}

func TestQueryTextAndLinkFilters(t *testing.T) {
	films := []Item{
		mkItem(1, "Alpha", `{"director":"Jane Campion","imdb_url":"https://www.imdb.com/title/tt0000001/"}`),
		mkItem(2, "Bravo", `{"director":"Campbell Ray","imdb_url":"https://www.imdb.com/title/tt1234567/"}`),
		mkItem(3, "Charlie", `{"director":"100% Real"}`),
		mkItem(4, "Delta", `{"director":"a_b Films","imdb_url":"https://www.imdb.com/title/tt0000009/"}`),
		mkItem(5, "Echo", `{"director":"Other"}`),
	}
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
			page, total := mustQuery(t, films, &movie, movieDefs, ListParams{Sort: SortTitle, FilterField: tt.field, FilterValue: tt.value})
			inOrderTitles(t, page, tt.want...)
			if total != len(tt.want) {
				t.Fatalf("total = %d, want %d", total, len(tt.want))
			}
		})
	}
}

func TestQueryNumberFilter(t *testing.T) {
	films := []Item{
		mkItem(1, "Old", `{"release_year":1979}`),
		mkItem(2, "Nineties", `{"release_year":1990}`),
		mkItem(3, "Millennium", `{"release_year":2000}`),
		mkItem(4, "Recent", `{"release_year":2010}`),
		mkItem(5, "Undated", ""),
	}
	tests := []struct {
		name string
		p    ListParams
		want []string
	}{
		{"min is inclusive and leaves out items without a value", ListParams{FilterField: "release_year", FilterMin: "1990"}, []string{"Nineties", "Millennium", "Recent"}},
		{"max is inclusive", ListParams{FilterField: "release_year", FilterMax: "2000"}, []string{"Old", "Nineties", "Millennium"}},
		{"min and max together are inclusive", ListParams{FilterField: "release_year", FilterMin: "1990", FilterMax: "2000"}, []string{"Nineties", "Millennium"}},
		{"equal bounds match exactly", ListParams{FilterField: "release_year", FilterMin: "1979", FilterMax: "1979"}, []string{"Old"}},
		{"empty range", ListParams{FilterField: "release_year", FilterMin: "1991", FilterMax: "1999"}, nil},
		{"decimal bound", ListParams{FilterField: "release_year", FilterMin: "1990.5"}, []string{"Millennium", "Recent"}},
		{"combined with the title search", ListParams{Q: "e", FilterField: "release_year", FilterMin: "1990", FilterMax: "2000"}, []string{"Nineties", "Millennium"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.p.Sort = SortTitle
			page, total := mustQuery(t, films, &movie, movieDefs, tt.p)
			sameTitles(t, page, tt.want...)
			if total != len(tt.want) {
				t.Fatalf("total = %d, want %d", total, len(tt.want))
			}
		})
	}

	t.Run("bad numbers and reversed bounds are invalid", func(t *testing.T) {
		for _, p := range []ListParams{
			{FilterField: "release_year", FilterMin: "abc"},
			{FilterField: "release_year", FilterMax: "1e"},
			{FilterField: "release_year", FilterMin: "2000", FilterMax: "1990"},
			{FilterField: "release_year", FilterMin: "0x1p-2"},
			{FilterField: "release_year", FilterMin: "1_000"},
		} {
			if _, _, err := Query(films, &movie, movieDefs, p); !errors.Is(err, ErrInvalidFilter) {
				t.Errorf("%+v: err = %v, want ErrInvalidFilter", p, err)
			}
		}
	})
}

func TestQueryDateFilter(t *testing.T) {
	concertType := "concerts"
	concertDefs := ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "date", Type: "date"}}}
	shows := []Item{
		mkItem(1, "Spring", `{"date":"2023-05-01"}`),
		mkItem(2, "New year", `{"date":"2024-01-01"}`),
		mkItem(3, "Summer", `{"date":"2024-06-30"}`),
		mkItem(4, "Winter", `{"date":"2025-12-31"}`),
		mkItem(5, "Undated", ""),
	}
	tests := []struct {
		name string
		p    ListParams
		want []string
	}{
		{"min is inclusive", ListParams{FilterMin: "2024-01-01"}, []string{"New year", "Summer", "Winter"}},
		{"max is inclusive", ListParams{FilterMax: "2024-06-30"}, []string{"Spring", "New year", "Summer"}},
		{"min and max together", ListParams{FilterMin: "2024-01-01", FilterMax: "2024-06-30"}, []string{"New year", "Summer"}},
		{"equal bounds", ListParams{FilterMin: "2024-06-30", FilterMax: "2024-06-30"}, []string{"Summer"}},
		{"after every date", ListParams{FilterMin: "2026-01-01"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.p.Sort, tt.p.FilterField = SortTitle, "date"
			page, total := mustQuery(t, shows, &concertType, concertDefs, tt.p)
			sameTitles(t, page, tt.want...)
			if total != len(tt.want) {
				t.Fatalf("total = %d, want %d", total, len(tt.want))
			}
		})
	}

	t.Run("invalid dates, reversed bounds and a value are invalid", func(t *testing.T) {
		for _, p := range []ListParams{
			{FilterField: "date", FilterMin: "2024-02-30"},
			{FilterField: "date", FilterMin: "yesterday"},
			{FilterField: "date", FilterMin: "2025-01-01", FilterMax: "2024-01-01"},
			{FilterField: "date", FilterValue: "2024-01-01"},
		} {
			if _, _, err := Query(shows, &concertType, concertDefs, p); !errors.Is(err, ErrInvalidFilter) {
				t.Errorf("%+v: err = %v, want ErrInvalidFilter", p, err)
			}
		}
	})
}

func TestQueryBooleanFilter(t *testing.T) {
	films := []Item{
		mkItem(1, "Yes", `{"lookup_disabled":true}`),
		mkItem(2, "No", `{"lookup_disabled":false}`),
		mkItem(3, "Unknown", ""),
	}
	t.Run("true and false select their items, unset is in neither", func(t *testing.T) {
		page, total := mustQuery(t, films, &movie, movieDefs, ListParams{FilterField: "lookup_disabled", FilterValue: "true"})
		sameTitles(t, page, "Yes")
		if total != 1 {
			t.Fatalf("true total = %d", total)
		}
		page, total = mustQuery(t, films, &movie, movieDefs, ListParams{FilterField: "lookup_disabled", FilterValue: "false"})
		sameTitles(t, page, "No")
		if total != 1 {
			t.Fatalf("false total = %d", total)
		}
	})
	t.Run("only true and false are values, and a boolean takes no range", func(t *testing.T) {
		for _, p := range []ListParams{
			{FilterField: "lookup_disabled", FilterValue: "yes"},
			{FilterField: "lookup_disabled", FilterValue: "True"},
			{FilterField: "lookup_disabled", FilterValue: "true", FilterMin: "1"},
		} {
			if _, _, err := Query(films, &movie, movieDefs, p); !errors.Is(err, ErrInvalidFilter) {
				t.Errorf("%+v: err = %v, want ErrInvalidFilter", p, err)
			}
		}
	})
}

func TestQueryStreamingFilterIgnoresLink(t *testing.T) {
	films := []Item{
		mkItem(1, "Alpha", `{"streaming":{"country":"IT","platforms":["Netflix"],"link":"https://www.justwatch.com/it/movie/alpha"}}`),
		mkItem(2, "Bravo", `{"streaming":{"country":"DE","platforms":["Prime Video"]}}`),
		mkItem(3, "Charlie", `{"streaming":{"country":"US"}}`),
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
			page, total := mustQuery(t, films, &movie, movieDefs, ListParams{Sort: SortTitle, FilterField: "streaming", FilterValue: tt.value})
			sameTitles(t, page, tt.want...)
			if total != len(tt.want) {
				t.Fatalf("total = %d, want %d", total, len(tt.want))
			}
		})
	}
}

// sortFixture is the T0 sort dataset: 1 alpha, 2 bravo, 3 charlie, 4 delta, 5 echo.
func sortFixture() []Item {
	alpha := mkItem(1, "Alpha", `{"release_year":1990,"runtime_minutes":120,"director":"Carl","lookup_disabled":true,`+
		`"imdb_url":"https://www.imdb.com/title/tt0000002/","streaming":{"country":"IT","platforms":["Netflix"],"link":"https://www.justwatch.com/it/movie/alpha"}}`)
	bravo := mkItem(2, "Bravo", `{"release_year":2000,"director":"ada","lookup_disabled":false,`+
		`"imdb_url":"https://www.imdb.com/title/tt0000001/","streaming":{"country":"DE"}}`)
	charlie := mkItem(3, "Charlie", `{"runtime_minutes":90}`)
	delta := mkItem(4, "Delta", `{"release_year":1990,"runtime_minutes":90,"director":"Bea","lookup_disabled":true,"streaming":{"country":"US"}}`)
	echo := mkItem(5, "Echo", `{"release_year":1985,"streaming":{"country":"IT","platforms":["Netflix"]}}`)
	return []Item{alpha, bravo, charlie, delta, echo}
}

func TestQuerySorts(t *testing.T) {
	films := sortFixture()
	tests := []struct {
		name, sort string
		want       []int
	}{
		// Numbers ascending, no value last, ties by id.
		{"number", "release_year", []int{5, 1, 4, 2, 3}},
		{"number with null ties by id", "runtime_minutes", []int{3, 4, 1, 2, 5}},
		// Text ascending, case-insensitive, no value last.
		{"text", "director", []int{2, 4, 1, 3, 5}},
		// Link sorts as text.
		{"link", "imdb_url", []int{2, 1, 3, 4, 5}},
		// Boolean: false before true, no value last.
		{"boolean", "lookup_disabled", []int{2, 1, 4, 3, 5}},
		// Streaming sorts by the object without link (jsonb text, case-insensitive).
		{"streaming", "streaming", []int{2, 1, 5, 4, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, total := mustQuery(t, films, &movie, movieDefs, ListParams{Sort: tt.sort})
			inOrderNums(t, page, tt.want...)
			if total != 5 {
				t.Fatalf("total = %d", total)
			}
		})
	}

	t.Run("updated is newest first", func(t *testing.T) {
		all := sortFixture()
		all[2].UpdatedAt = base.Add(100 * time.Second) // charlie was just patched
		page, _ := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: SortUpdated})
		inOrderNums(t, page, 3, 5, 4, 2, 1)
	})

	t.Run("an empty text value sorts last like a missing one", func(t *testing.T) {
		all := []Item{
			mkItem(1, "a", `{"director":""}`),
			mkItem(2, "b", ""),
			mkItem(3, "c", `{"director":"Zed"}`),
			mkItem(4, "d", `{"director":"Ann"}`),
		}
		page, _ := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: "director"})
		inOrderNums(t, page, 4, 3, 1, 2)
	})

	t.Run("date sorts as text, no value last, ties by id", func(t *testing.T) {
		concertType := "concerts"
		defs := ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "date", Type: "date"}}}
		list := []Item{
			mkItem(1, "Late", `{"date":"2024-05-01"}`),
			mkItem(2, "Early", `{"date":"2023-01-01"}`),
			mkItem(3, "Undated", ""),
			mkItem(4, "Same", `{"date":"2024-05-01"}`),
		}
		page, _ := mustQuery(t, list, &concertType, defs, ListParams{Sort: "date"})
		inOrderNums(t, page, 2, 1, 4, 3)
	})

	t.Run("title sort is case-insensitive", func(t *testing.T) {
		all := []Item{mkItem(1, "banana", ""), mkItem(2, "Apple", ""), mkItem(3, "cherry", "")}
		page, _ := mustQuery(t, all, nil, ListDefs{SortMode: SortManual}, ListParams{Sort: SortTitle})
		inOrderTitles(t, page, "Apple", "banana", "cherry")
	})
}

func TestQueryUpdatedSortOfListItems(t *testing.T) {
	all := []Item{mkItem(1, "banana", ""), mkItem(2, "Apple", ""), mkItem(3, "cherry", "")}
	all[0].UpdatedAt = base.Add(50 * time.Second) // banana was touched last
	page, _ := mustQuery(t, all, nil, ListDefs{SortMode: SortManual}, ListParams{Sort: SortUpdated})
	inOrderTitles(t, page, "banana", "cherry", "Apple")
}

func TestQueryDirectorSortAndFilter(t *testing.T) {
	films := []Item{
		mkItem(1, "Zulu", `{"director":"Jane Campion"}`),
		mkItem(2, "Alpha", `{"director":"Jane Campion"}`),
		mkItem(3, "Middle", `{"director":"Bong Joon-ho"}`),
		mkItem(4, "No director", ""),
	}
	page, total := mustQuery(t, films, &movie, movieDefs, ListParams{Sort: "director"})
	if got := nums(page); len(got) != 4 || got[0] != 3 || got[3] != 4 || total != 4 {
		t.Fatalf("director sort = %v total=%d, want Bong Joon-ho first and missing directors last", titlesOf(page), total)
	}
	if got := nums(page)[1:3]; !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("director sort ties = %v, want Zulu and Alpha by id", got)
	}

	page, total = mustQuery(t, films, &movie, movieDefs, ListParams{Sort: "director", Q: "campion"})
	if total != 2 {
		t.Fatalf("director filter total = %d, want 2", total)
	}
	inOrderNums(t, page, 1, 2)

	tv := "tv_show"
	tvDefs := ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "end_year", Type: "number"}}}
	if _, _, err := Query(films, &tv, tvDefs, ListParams{Sort: "director"}); !errors.Is(err, ErrInvalidSort) {
		t.Fatalf("director sort on a type without it: err = %v, want ErrInvalidSort", err)
	}
}

func TestQueryManualOrderWithFilter(t *testing.T) {
	m1 := mkItem(1, "m1", `{"lookup_disabled":true}`)
	m2 := mkItem(2, "m2", `{"lookup_disabled":false}`)
	m3 := mkItem(3, "m3", `{"lookup_disabled":true}`)
	m3.SortOrder, m1.SortOrder, m2.SortOrder = 0, 1, 2 // the order the user set
	all := []Item{m1, m2, m3}

	page, _ := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: SortManual})
	inOrderTitles(t, page, "m3", "m1", "m2")
	page, _ = mustQuery(t, all, &movie, movieDefs, ListParams{Sort: SortManual, FilterField: "lookup_disabled", FilterValue: "true"})
	inOrderTitles(t, page, "m3", "m1")
}

func TestQueryPriceSort(t *testing.T) {
	wishlist := "wishlist"
	defs := ListDefs{SortMode: SortManual, AllowsPrice: true}
	nopriceB := mkItem(1, "b-none", "")
	nopriceA := mkItem(2, "A-none", "")
	exp := withPrice(mkItem(3, "expensive", ""), "1299.00")
	rng := withPrice(mkItem(4, "range", ""), "20.50") // min 20.50 sorts before a single 25.00
	rng.Price.Max = strPtr("500.00")
	cheap := withPrice(mkItem(5, "Cheap", ""), "9.99")
	tieB := withPrice(mkItem(6, "tie-b", ""), "25.00")
	tieA := withPrice(mkItem(7, "Tie-a", ""), "25.00")
	tieA.Price.Max = strPtr("25.00")
	all := []Item{nopriceB, nopriceA, exp, rng, cheap, tieB, tieA}
	want := []string{"Cheap", "range", "Tie-a", "tie-b", "expensive", "A-none", "b-none"}

	t.Run("order: min ascending, no price last, ties by title", func(t *testing.T) {
		page, _ := mustQuery(t, all, &wishlist, defs, ListParams{Sort: "price"})
		inOrderTitles(t, page, want...)
	})

	t.Run("paging is stable", func(t *testing.T) {
		var got []string
		for off := 0; off < len(want); off += 3 {
			page, total := mustQuery(t, all, &wishlist, defs, ListParams{Sort: "price", Limit: 3, Offset: off})
			if total != len(want) {
				t.Fatalf("total = %d", total)
			}
			got = append(got, titlesOf(page)...)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("paged: got %v, want %v", got, want)
		}
	})

	t.Run("stored sort_mode price is the default, override wins", func(t *testing.T) {
		stored := ListDefs{SortMode: "price", AllowsPrice: true}
		page, _ := mustQuery(t, all, &wishlist, stored, ListParams{})
		inOrderTitles(t, page, want...)
		page, _ = mustQuery(t, all, &wishlist, stored, ListParams{Sort: SortTitle})
		if titlesOf(page)[0] != "A-none" {
			t.Fatalf("override title: got %v", titlesOf(page))
		}
	})

	t.Run("price sort on a type without price is invalid", func(t *testing.T) {
		if _, _, err := Query(all, nil, ListDefs{SortMode: SortManual}, ListParams{Sort: "price"}); !errors.Is(err, ErrInvalidSort) {
			t.Fatalf("err = %v, want ErrInvalidSort", err)
		}
	})
}

func strPtr(s string) *string { return &s }

func TestQueryFieldSortAndCombinedFilter(t *testing.T) {
	rated := "rated"
	defs := ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "rating", Type: "number"}}}
	all := []Item{
		mkItem(1, "Match ten", `{"rating":10}`),
		mkItem(2, "Match two", `{"rating":2}`),
		mkItem(3, "Match five", `{"rating":5}`),
		mkItem(4, "Match missing", ""),
	}
	page, _ := mustQuery(t, all, &rated, defs, ListParams{Sort: "rating"})
	inOrderTitles(t, page, "Match two", "Match five", "Match ten", "Match missing")

	page, total := mustQuery(t, all, &rated, defs, ListParams{Sort: "rating", Q: "match", FilterField: "rating", FilterMin: "4", FilterMax: "10"})
	inOrderTitles(t, page, "Match five", "Match ten")
	if total != 2 {
		t.Fatalf("combined total = %d", total)
	}

	if _, _, err := Query(all, &rated, defs, ListParams{FilterField: "rating", FilterMin: "10", FilterMax: "2"}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("reversed range: err = %v", err)
	}
}

func TestQueryFieldSortOrdersByDefinition(t *testing.T) {
	// The ListItemsSortsByDefinitionAndFilters fixture: late 2005, none, early 1990,
	// tie 1990, mid 2000. Items of equal year break on id.
	all := []Item{
		mkItem(1, "late", `{"release_year":2005}`),
		mkItem(2, "none", ""),
		mkItem(3, "early", `{"release_year":1990}`),
		mkItem(4, "tie", `{"release_year":1990}`),
		mkItem(5, "mid", `{"release_year":2000}`),
	}

	t.Run("explicit sort, ascending, empty last, ties by id", func(t *testing.T) {
		page, _ := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: "release_year"})
		inOrderNums(t, page, 3, 4, 5, 1, 2)
	})

	t.Run("paging keeps the order and the total", func(t *testing.T) {
		page, total := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: "release_year", Limit: 2, Offset: 2})
		inOrderNums(t, page, 5, 1)
		if total != 5 {
			t.Fatalf("total = %d", total)
		}
	})

	t.Run("q narrows the sorted list", func(t *testing.T) {
		page, total := mustQuery(t, all, &movie, movieDefs, ListParams{Sort: "release_year", Q: "LATE"})
		inOrderTitles(t, page, "late")
		if total != 1 {
			t.Fatalf("total = %d", total)
		}
	})

	t.Run("the list's default sort is its sort_mode field, the explicit manual wins", func(t *testing.T) {
		stored := ListDefs{SortMode: "release_year", Fields: movieFields}
		page, _ := mustQuery(t, all, &movie, stored, ListParams{})
		inOrderNums(t, page, 3, 4, 5, 1, 2)
		page, _ = mustQuery(t, all, &movie, stored, ListParams{Sort: SortManual})
		inOrderNums(t, page, 1, 2, 3, 4, 5)
	})

	t.Run("invalid sorts are ErrInvalidSort", func(t *testing.T) {
		withStores := ListDefs{SortMode: SortManual, Fields: append(slices.Clone(movieFields), types.Field{Key: "stores", Type: "stores"})}
		for _, sort := range []string{"nope", "stores", "type_key"} {
			if _, _, err := Query(all, &movie, withStores, ListParams{Sort: sort}); !errors.Is(err, ErrInvalidSort) {
				t.Errorf("sort %s: err = %v, want ErrInvalidSort", sort, err)
			}
		}
		if _, _, err := Query(all, nil, ListDefs{SortMode: SortManual}, ListParams{Sort: "type_key"}); !errors.Is(err, ErrInvalidSort) {
			t.Errorf("no-type list: err = %v, want ErrInvalidSort", err)
		}
	})
}

func TestQueryLimitOffsetTotalWithFilter(t *testing.T) {
	var all []Item
	for i, title := range []string{"p1", "p2", "p3", "p4", "p5"} {
		meta := ""
		if i < 4 {
			meta = fmt.Sprintf(`{"release_year":%d}`, 1990+i)
		}
		all = append(all, mkItem(i+1, title, meta))
	}
	filter := ListParams{FilterField: "release_year", FilterMin: "1990"}

	p := filter
	p.Sort, p.Limit = "release_year", 3
	page, total := mustQuery(t, all, &movie, movieDefs, p)
	inOrderTitles(t, page, "p1", "p2", "p3")
	if total != 4 {
		t.Fatalf("page 1 total = %d", total)
	}
	p.Offset = 3
	page, total = mustQuery(t, all, &movie, movieDefs, p)
	inOrderTitles(t, page, "p4")
	if total != 4 {
		t.Fatalf("page 2 total = %d", total)
	}
	p.Offset = 4
	page, total = mustQuery(t, all, &movie, movieDefs, p)
	if len(page) != 0 || total != 4 {
		t.Fatalf("past the end: %v total %d", titlesOf(page), total)
	}
	p = filter
	p.Sort, p.Q, p.Limit, p.Offset = SortTitle, "p", 2, 2
	page, total = mustQuery(t, all, &movie, movieDefs, p)
	inOrderTitles(t, page, "p3", "p4")
	if total != 4 {
		t.Fatalf("q with filter total = %d", total)
	}
}

func TestQueryPagesAreStableAndDisjoint(t *testing.T) {
	var all []Item
	for i := range 7 {
		all = append(all, mkItem(i+1, "same", "")) // equal titles: the id breaks the tie
	}
	seen := map[int]bool{}
	for off := 0; off < 7; off += 3 {
		page, total := mustQuery(t, all, nil, ListDefs{SortMode: SortManual}, ListParams{Sort: SortTitle, Limit: 3, Offset: off})
		if total != 7 {
			t.Fatalf("total = %d", total)
		}
		for _, n := range nums(page) {
			if seen[n] {
				t.Fatalf("item %d on two pages", n)
			}
			seen[n] = true
		}
	}
	if len(seen) != 7 {
		t.Fatalf("saw %d distinct items, want 7", len(seen))
	}
}

func TestQueryDefaultsAndPageBounds(t *testing.T) {
	var all []Item
	for i := range 3 {
		all = append(all, mkItem(i+1, "t", ""))
	}
	page, total := mustQuery(t, all, nil, ListDefs{SortMode: SortManual}, ListParams{Offset: 10})
	if len(page) != 0 || total != 3 || page == nil {
		t.Fatalf("past the end: page=%v nil=%v total=%d", page, page == nil, total)
	}
	page, _ = mustQuery(t, all, nil, ListDefs{SortMode: SortManual}, ListParams{Limit: 500})
	if len(page) != 3 {
		t.Fatalf("limit above MaxLimit still returns the rows: %d", len(page))
	}
}

func TestNormalizedParams(t *testing.T) {
	tests := []struct {
		name string
		in   ListParams
		want ListParams
	}{
		{"zero values are the defaults", ListParams{}, ListParams{Sort: SortManual, Limit: DefaultLimit}},
		{"limit above the maximum is clamped", ListParams{Limit: 500}, ListParams{Sort: SortManual, Limit: MaxLimit}},
		{"limit 200 is kept", ListParams{Limit: 200}, ListParams{Sort: SortManual, Limit: 200}},
		{"negative limit and offset", ListParams{Limit: -3, Offset: -1}, ListParams{Sort: SortManual, Limit: DefaultLimit}},
		{"explicit sort is kept", ListParams{Sort: SortTitle, Offset: 4, Limit: 2}, ListParams{Sort: SortTitle, Offset: 4, Limit: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.Normalized(SortManual)
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if again := got.Normalized(SortTitle); again != got {
				t.Fatalf("not idempotent: %+v", again)
			}
		})
	}
}

func TestQueryInvalidSortAndFilterAreRejected(t *testing.T) {
	films := []Item{mkItem(1, "x", "")}
	books := "book"
	bookDefs := ListDefs{SortMode: SortManual, Fields: []types.Field{
		{Key: "author", Type: "text"}, {Key: "pages", Type: "number"}, {Key: "stores", Type: "stores"},
	}}
	plain := []Item{mkItem(1, "x", "")}

	tests := []struct {
		name string
		all  []Item
		typ  *string
		defs ListDefs
		p    ListParams
		want error
	}{
		{"price sort on a type without price", films, &movie, movieDefs, ListParams{Sort: "price"}, ErrInvalidSort},
		{"sort on a stores field", films, &books, bookDefs, ListParams{Sort: "stores"}, ErrInvalidSort},
		{"filter on a stores field", films, &books, bookDefs, ListParams{FilterField: "stores", FilterValue: "amazon"}, ErrInvalidFilter},
		{"filter value without a field", films, &movie, movieDefs, ListParams{FilterValue: "x"}, ErrInvalidFilter},
		{"filter min without a field", films, &movie, movieDefs, ListParams{FilterMin: "1990"}, ErrInvalidFilter},
		{"filter max without a field", films, &movie, movieDefs, ListParams{FilterMax: "1990"}, ErrInvalidFilter},
		{"bad number", films, &movie, movieDefs, ListParams{FilterField: "release_year", FilterMin: "abc"}, ErrInvalidFilter},
		{"number field without a bound", films, &movie, movieDefs, ListParams{FilterField: "release_year"}, ErrInvalidFilter},
		{"text field with a range", films, &movie, movieDefs, ListParams{FilterField: "director", FilterMin: "1"}, ErrInvalidFilter},
		{"text field with a value and a range", films, &movie, movieDefs, ListParams{FilterField: "director", FilterValue: "x", FilterMin: "1"}, ErrInvalidFilter},
		{"boolean field with a bad value", films, &movie, movieDefs, ListParams{FilterField: "lookup_disabled", FilterValue: "maybe"}, ErrInvalidFilter},
		{"unknown filter field", films, &movie, movieDefs, ListParams{FilterField: "nope", FilterValue: "x"}, ErrInvalidFilter},
		{"filter on a list without a type", plain, nil, ListDefs{SortMode: SortManual}, ListParams{FilterField: "anything", FilterValue: "x"}, ErrInvalidFilter},
		{"unknown sort field", films, &movie, movieDefs, ListParams{Sort: "nope"}, ErrInvalidSort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Query(tt.all, tt.typ, tt.defs, tt.p); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestQueryMetadataDecodeErrorIsReturned(t *testing.T) {
	bad := []Item{{ID: uuid.UUID{15: 1}, Metadata: json.RawMessage(`{"a":`)}}
	if _, _, err := Query(bad, nil, ListDefs{SortMode: SortManual}, ListParams{}); err == nil || errors.Is(err, ErrInvalidSort) {
		t.Fatalf("err = %v, want a decode error", err)
	}
}

func TestJSONTextMatchesJSONB(t *testing.T) {
	// jsonb prints keys shorter first (then bytewise), ": " and ", " separators.
	var obj map[string]any
	if err := json.Unmarshal([]byte(`{"paid_platforms":["Prime Video"],"platforms":["Netflix","Prime Video"],"country":"IT","link":"https://www.justwatch.com/x"}`), &obj); err != nil {
		t.Fatal(err)
	}
	got, ok := streamingText(obj)
	if !ok {
		t.Fatal("streaming object must have a text")
	}
	want := `{"country": "IT", "platforms": ["Netflix", "Prime Video"], "paid_platforms": ["Prime Video"]}`
	if got != want {
		t.Fatalf("streamingText = %s, want %s", got, want)
	}

	// Strings use Postgres escapes: no escaping of <, >, & or /, but \" \\ and control characters.
	var esc map[string]any
	if err := json.Unmarshal([]byte(`{"a":"x\"y\\z\n\u0001<&>/"}`), &esc); err != nil {
		t.Fatal(err)
	}
	if got := jsonText(esc); got != `{"a": "x\"y\\z\n\u0001<&>/"}` {
		t.Fatalf("jsonText = %s", got)
	}

	if s, ok := streamingText(nil); ok || s != "" {
		t.Fatalf("a missing streaming value is NULL, got %q %v", s, ok)
	}
	if s, ok := streamingText("plain"); !ok || s != "plain" {
		t.Fatalf("a non-object streaming value is its text, got %q %v", s, ok)
	}
	if !strings.Contains(jsonText([]any{true, nil}), "[true, null]") {
		t.Fatal("arrays are printed with ', '")
	}
}

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike(`50%_\x`); got != `50\%\_\\x` {
		t.Fatalf("got %s", got)
	}
}
