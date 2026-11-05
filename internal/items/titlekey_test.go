package items

import (
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F026-B2: the provider id on movie, TV and book lists; the title elsewhere.
func TestF026B2_SameEntry(t *testing.T) {
	mov, book := types.Movie, types.Book
	dune84 := TitleEntry{Title: "Dune", Metadata: []byte(`{"tmdb_id":841}`)}
	dune21 := TitleEntry{Title: "Dune", Metadata: []byte(`{"tmdb_id":438631}`)}
	tests := []struct {
		name string
		typ  *string
		a, b TitleEntry
		want bool
	}{
		{"remakes share a title", &mov, dune84, dune21, false},
		{"same tmdb id, other title", &mov, dune21, TitleEntry{Title: "Dune: Part One", Metadata: []byte(`{"tmdb_id":438631}`)}, true},
		{"no id never clashes", &mov, TitleEntry{Title: "Dune"}, dune21, false},
		{"two without id never clash", &mov, TitleEntry{Title: "Dune"}, TitleEntry{Title: "Dune"}, false},
		{"books by openlibrary id", &book, TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL893415W"}`)}, TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL1W"}`)}, false},
		{"same book id", &book, TitleEntry{Title: "Dune", Metadata: []byte(`{"openlibrary_id":"OL1W"}`)}, TitleEntry{Title: "Duna", Metadata: []byte(`{"openlibrary_id":"ol1w"}`)}, true},
		{"generic: accents and case", nil, TitleEntry{Title: "Amélie"}, TitleEntry{Title: " amelie "}, true},
		{"generic: other titles", nil, TitleEntry{Title: "Heat"}, TitleEntry{Title: "Ran"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameEntry(tt.typ, tt.a, tt.b); got != tt.want {
				t.Errorf("sameEntry = %v, want %v", got, tt.want)
			}
		})
	}
}
