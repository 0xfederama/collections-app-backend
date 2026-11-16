package items

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F-028 B3 (D-163): sort=rating, the genre filter and the genres of a list.

var (
	ratedDefs = ListDefs{SortMode: SortManual, Fields: []types.Field{{Key: "tmdb_rating", Type: "number"}, {Key: "genres", Type: "text_list"}}}
	tvType    = types.TVShow
	bookType  = types.Book
)

func TestF028B3_RatingSortIsDescendingWithUnratedLast(t *testing.T) {
	all := []Item{
		mkItem(1, "Low", `{"tmdb_rating":5.1}`),
		mkItem(2, "None", ""),
		mkItem(3, "High", `{"tmdb_rating":8.4}`),
		mkItem(4, "Tie A", `{"tmdb_rating":8.4}`),
		mkItem(5, "Zero", `{"tmdb_rating":0}`),
		mkItem(6, "Null", `{"tmdb_rating":null}`),
	}
	page, total := mustQuery(t, all, &movie, ratedDefs, ListParams{Sort: "rating"})
	// Highest first; ties by id (3 before 4); nulls (2, 6) last, by id.
	if total != 6 || !reflect.DeepEqual(nums(page), []int{3, 4, 1, 5, 2, 6}) {
		t.Fatalf("rating order = %v (total %d), want [3 4 1 5 2 6]", nums(page), total)
	}
	// The same on a TV list.
	page, _ = mustQuery(t, all, &tvType, ratedDefs, ListParams{Sort: "rating"})
	if !reflect.DeepEqual(nums(page), []int{3, 4, 1, 5, 2, 6}) {
		t.Errorf("TV rating order = %v", nums(page))
	}
	// A list whose sort_mode is rating sorts so by default.
	page, _ = mustQuery(t, all, &movie, ListDefs{SortMode: SortRating, Fields: ratedDefs.Fields}, ListParams{})
	if !reflect.DeepEqual(nums(page), []int{3, 4, 1, 5, 2, 6}) {
		t.Errorf("sort_mode rating order = %v", nums(page))
	}
}

func TestF028B3_RatingSortPages(t *testing.T) {
	all := []Item{
		mkItem(1, "A", `{"tmdb_rating":9}`), mkItem(2, "B", `{"tmdb_rating":8}`),
		mkItem(3, "C", `{"tmdb_rating":7}`), mkItem(4, "D", ""),
	}
	page, total := mustQuery(t, all, &movie, ratedDefs, ListParams{Sort: "rating", Limit: 2, Offset: 1})
	if total != 4 || !reflect.DeepEqual(nums(page), []int{2, 3}) {
		t.Errorf("page 2 of 2 = %v total %d, want [2 3] total 4", nums(page), total)
	}
	page, _ = mustQuery(t, all, &movie, ratedDefs, ListParams{Sort: "rating", Limit: 2, Offset: 3})
	if !reflect.DeepEqual(nums(page), []int{4}) {
		t.Errorf("last page = %v, want the unrated item", nums(page))
	}
}

func TestF028B3_RatingSortIsNotForOtherTypes(t *testing.T) {
	all := []Item{mkItem(1, "A", `{"tmdb_rating":9}`)}
	for _, typ := range []*string{nil, &bookType} {
		if _, _, err := Query(all, typ, ListDefs{SortMode: SortManual}, ListParams{Sort: "rating"}); !errors.Is(err, ErrRatingSort) {
			t.Errorf("rating sort on %v: err = %v, want ErrRatingSort", typ, err)
		}
	}
}

func TestF028B3_GenreFilterAnyOfCaseInsensitiveAndTrimmed(t *testing.T) {
	all := []Item{
		mkItem(1, "Heat", `{"genres":["Crime","Thriller"]}`),
		mkItem(2, "Alien", `{"genres":["Science Fiction"]}`),
		mkItem(3, "Plain", ""),
		mkItem(4, "Noir", `{"genres":[" crime "]}`),
	}
	for name, tc := range map[string]struct {
		genres []string
		want   []int
	}{
		"one genre, any case":       {[]string{"CRIME"}, []int{1, 4}},
		"any of two genres":         {[]string{"science fiction", "thriller"}, []int{1, 2}},
		"values are trimmed":        {[]string{"  Science Fiction  "}, []int{2}},
		"no match":                  {[]string{"Western"}, nil},
		"substring is not a match":  {[]string{"Sci"}, nil},
		"no genre filter keeps all": {nil, []int{1, 2, 3, 4}},
	} {
		t.Run(name, func(t *testing.T) {
			page, total := mustQuery(t, all, &movie, ratedDefs, ListParams{Genres: tc.genres})
			if !reflect.DeepEqual(nums(page), tc.want) && !(len(tc.want) == 0 && len(page) == 0) || total != len(tc.want) {
				t.Errorf("genre %v = %v (total %d), want %v", tc.genres, nums(page), total, tc.want)
			}
		})
	}
}

func TestF028B3_GenreFilterCombinesWithTitleAndPagesAfterFiltering(t *testing.T) {
	all := []Item{
		mkItem(1, "Crime Wave", `{"genres":["Drama"]}`),
		mkItem(2, "Crime Doc", `{"genres":["Crime"]}`),
		mkItem(3, "Crime Story", `{"genres":["Crime"]}`),
		mkItem(4, "Other", `{"genres":["Crime"]}`),
	}
	// q and the genre filter are ANDed, and the page is cut from the filtered rows.
	page, total := mustQuery(t, all, &movie, ratedDefs, ListParams{Q: "crime", Genres: []string{"crime"}, Limit: 1, Offset: 1})
	if total != 2 || !reflect.DeepEqual(nums(page), []int{3}) {
		t.Errorf("page = %v total %d, want [3] of 2", nums(page), total)
	}
}

func TestF028B3_GenreFilterRules(t *testing.T) {
	all := []Item{mkItem(1, "A", `{"genres":["Drama"]}`)}
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = "Drama"
	}
	for name, tc := range map[string]struct {
		typ    *string
		genres []string
	}{
		"not on a book list":         {&bookType, []string{"Drama"}},
		"not on a list with no type": {nil, []string{"Drama"}},
		"eleven values":              {&movie, tooMany},
		"a blank value":              {&movie, []string{"   "}},
		"a value over 100":           {&movie, []string{strings.Repeat("a", 101)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Query(all, tc.typ, ratedDefs, ListParams{Genres: tc.genres}); !errors.Is(err, ErrInvalidGenre) {
				t.Errorf("err = %v, want ErrInvalidGenre", err)
			}
		})
	}
	// Ten values and exactly 100 characters are accepted.
	ten := make([]string, 10)
	for i := range ten {
		ten[i] = "Drama"
	}
	if _, _, err := Query(all, &movie, ratedDefs, ListParams{Genres: ten}); err != nil {
		t.Errorf("ten values: %v", err)
	}
	if _, _, err := Query(all, &movie, ratedDefs, ListParams{Genres: []string{strings.Repeat("a", 100)}}); err != nil {
		t.Errorf("100 characters: %v", err)
	}
}

func TestF028B3_GenresAreNotSortable(t *testing.T) {
	all := []Item{mkItem(1, "A", `{"genres":["Drama"]}`)}
	if _, _, err := Query(all, &movie, ratedDefs, ListParams{Sort: "genres"}); !errors.Is(err, ErrInvalidSort) {
		t.Errorf("genres sort: err = %v, want ErrInvalidSort", err)
	}
}

func TestF028B3_GenreCountsOncePerItemFirstSpellingSortedByName(t *testing.T) {
	all := []Item{
		mkItem(1, "A", `{"genres":["Science Fiction","drama"]}`),
		mkItem(2, "B", `{"genres":["Drama","Action","Drama"]}`),
		mkItem(3, "C", `{"genres":[" science fiction "]}`),
		mkItem(4, "D", ""),
	}
	got, err := GenreCounts(all, &movie)
	if err != nil {
		t.Fatal(err)
	}
	want := []GenreCount{
		{Name: "Action", Count: 1},
		{Name: "drama", Count: 2},
		{Name: "Science Fiction", Count: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

func TestF028B3_GenreCountsEmptyForOtherTypes(t *testing.T) {
	all := []Item{mkItem(1, "A", `{"genres":["Drama"]}`)}
	for _, typ := range []*string{nil, &bookType} {
		got, err := GenreCounts(all, typ)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("genres on %v = %#v err %v, want an empty non-nil slice", typ, got, err)
		}
	}
}
