package items

import (
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

func TestKeepProviderIDsOnPatch(t *testing.T) {
	movie, _ := types.Lookup("movie")
	book, _ := types.Lookup("book")
	typ := func(s string) *string { return &s }
	tests := []struct {
		name   string
		typ    string
		fields []types.Field
		in     map[string]any
		stored map[string]any
		key    string
		want   any
		absent bool
	}{
		{"omitted tmdb_id is kept", "movie", movie.Fields, map[string]any{"director": "X"}, map[string]any{"tmdb_id": 603.0, "director": "Y"}, "tmdb_id", 603.0, false},
		{"omitted lookup_disabled is kept", "movie", movie.Fields, map[string]any{}, map[string]any{"lookup_disabled": true}, "lookup_disabled", true, false},
		{"explicit tmdb_id is written", "movie", movie.Fields, map[string]any{"tmdb_id": 604.0}, map[string]any{"tmdb_id": 603.0}, "tmdb_id", 604.0, false},
		{"explicit null clears tmdb_id", "movie", movie.Fields, map[string]any{"tmdb_id": nil}, map[string]any{"tmdb_id": 603.0}, "tmdb_id", nil, true},
		{"omitted openlibrary_id is kept", "book", book.Fields, map[string]any{"author": "A"}, map[string]any{"openlibrary_id": "OL1W"}, "openlibrary_id", "OL1W", false},
		{"nothing stored stays absent", "movie", movie.Fields, map[string]any{}, map[string]any{}, "tmdb_id", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ValidateMetadata(typ(tt.typ), tt.fields, tt.in, tt.stored)
			if err != nil {
				t.Fatalf("ValidateMetadata: %v", err)
			}
			v, ok := out[tt.key]
			if tt.absent {
				if ok && v != nil {
					t.Fatalf("%s = %v, want absent", tt.key, v)
				}
				return
			}
			if !sameJSONValue(v, tt.want) {
				t.Fatalf("%s = %v, want %v", tt.key, v, tt.want)
			}
		})
	}
}
