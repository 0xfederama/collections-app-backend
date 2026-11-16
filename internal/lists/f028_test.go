package lists

import "testing"

// F-028 (D-162): genres (text_list) cannot be a list's sort mode; the score (number) can.
func TestF028B1_TextListIsNoSortMode(t *testing.T) {
	movie := "movie"
	fields := []FieldInfo{{Key: "genres", FieldType: "text_list"}, {Key: "tmdb_rating", FieldType: "number"}}
	if err := validateDefinitionSort(&movie, "genres", false, fields, "sort_mode"); err == nil {
		t.Error("genres accepted as a sort mode")
	}
	if err := validateDefinitionSort(&movie, "tmdb_rating", false, fields, "sort_mode"); err != nil {
		t.Errorf("tmdb_rating sort mode: %v", err)
	}
}

// F-028 B3 (D-163): rating is a sort mode of the movie and TV types only.
func TestF028B3_RatingIsASortModeOfMovieAndTVOnly(t *testing.T) {
	movie, tv, book := "movie", "tv_show", "book"
	for _, tc := range []struct {
		name string
		typ  *string
		ok   bool
	}{
		{"movie", &movie, true},
		{"tv", &tv, true},
		{"book", &book, false},
		{"no type", nil, false},
	} {
		err := validateDefinitionSort(tc.typ, "rating", false, nil, "sort_mode")
		if (err == nil) != tc.ok {
			t.Errorf("%s: rating sort_mode err = %v, want accepted=%v", tc.name, err, tc.ok)
		}
	}
}
