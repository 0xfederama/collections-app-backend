package lookup

import (
	"errors"
	"reflect"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

func TestF016B3_ApplyRejectsProviderTypeMismatchesAndBadIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ApplyParams
		ok   bool
		want string
	}{
		{"book on openlibrary", ApplyParams{Provider: ProviderOpenLibrary, Type: "book", SourceID: "OL1W", Title: "x", Mode: ModeFill}, true, ""},
		{"movie on tmdb", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "1", Title: "x", Mode: ModeFill}, true, ""},
		{"book on tmdb", ApplyParams{Provider: ProviderTMDB, Type: "book", SourceID: "1", Title: "x", Mode: ModeFill}, false, "type"},
		{"movie on openlibrary", ApplyParams{Provider: ProviderOpenLibrary, Type: "movie", SourceID: "OL1W", Title: "x", Mode: ModeFill}, false, "type"},
		{"unknown provider", ApplyParams{Provider: "google", Type: "book", SourceID: "1", Title: "x", Mode: ModeFill}, false, "type"},
		{"bad work id", ApplyParams{Provider: ProviderOpenLibrary, Type: "book", SourceID: "OL1", Title: "x", Mode: ModeFill}, false, "source_id"},
		{"bad tmdb id", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "0", Title: "x", Mode: ModeFill}, false, "tmdb_id"},
		{"too large tmdb id", ApplyParams{Provider: ProviderTMDB, Type: "movie", SourceID: "2147483648", Title: "x", Mode: ModeFill}, false, "tmdb_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApply(tc.p)
			if tc.ok {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var validation *apperr.ValidationError
			if !errors.As(err, &validation) || validation.Errors[0].Field != tc.want {
				t.Fatalf("err = %v, want a validation error on %q", err, tc.want)
			}
		})
	}
}

func TestFillOnlyHelper(t *testing.T) {
	for _, tc := range []struct {
		name       string
		metadata   map[string]any
		emptyLists bool
		want       any
	}{
		{"absent", map[string]any{}, false, []any{"amazon"}},
		{"null", map[string]any{"stores": nil}, false, []any{"amazon"}},
		{"empty list kept on create", map[string]any{"stores": []any{}}, false, []any{}},
		{"empty list filled on fill", map[string]any{"stores": []any{}}, true, []any{"amazon"}},
		{"user choice kept", map[string]any{"stores": []any{"kobo"}}, true, []any{"kobo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fillOnly(tc.metadata, map[string]any{"stores": []string{"amazon"}}, tc.emptyLists)
			if !reflect.DeepEqual(tc.metadata["stores"], tc.want) {
				t.Errorf("stores = %#v, want %#v", tc.metadata["stores"], tc.want)
			}
		})
	}
}
