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
