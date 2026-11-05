package items

import "testing"

// F026-B2: provider identity wins over the title when both items have one.
func TestF026B2_SameEntry(t *testing.T) {
	dune84 := TitleEntry{Title: "Dune", Metadata: []byte(`{"tmdb_id":841}`)}
	dune21 := TitleEntry{Title: "Dune", Metadata: []byte(`{"tmdb_id":438631}`)}
	tests := []struct {
		name string
		a, b TitleEntry
		want bool
	}{
		{"remakes share a title", dune84, dune21, false},
		{"same tmdb id, other title", dune21, TitleEntry{Title: "Dune: Part One", Metadata: []byte(`{"tmdb_id":438631}`)}, true},
		{"one side without id: title rule", TitleEntry{Title: " DUNE "}, dune21, true},
		{"no ids, accents and case", TitleEntry{Title: "Amélie"}, TitleEntry{Title: "amelie"}, true},
		{"no ids, other titles", TitleEntry{Title: "Heat"}, TitleEntry{Title: "Ran"}, false},
		{"books by openlibrary id", TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL893415W"}`)}, TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL1W"}`)}, false},
		{"same book id", TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL1W"}`)}, TitleEntry{Title: "Duna", Metadata: []byte(`{"openlibrary_id":"ol1w"}`)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameEntry(tt.a, tt.b); got != tt.want {
				t.Errorf("sameEntry = %v, want %v", got, tt.want)
			}
		})
	}
}
