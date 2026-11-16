package items

import (
	"errors"
	"reflect"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F-028 (D-162): the imported score, vote count and genres are the lookup's. A client may not change
// them, a value it sends equal to the stored one is accepted, and an omitted one keeps the stored value.

func f028Movie(t *testing.T) []types.Field {
	t.Helper()
	def, ok := types.Lookup(types.Movie)
	if !ok {
		t.Fatal("movie type missing")
	}
	return def.Fields
}

func TestF028B1_CreateRefusesLookupOwnedValues(t *testing.T) {
	fields := f028Movie(t)
	for _, in := range []map[string]any{
		{"genres": []any{"Western"}},
		{"tmdb_rating": 9.0},
		{"tmdb_vote_count": 3},
	} {
		_, err := ValidateMetadata(&movieType, fields, in, nil)
		var field string
		if got := validationFields(err); len(got) == 1 {
			field = got[0]
		}
		if field == "" {
			t.Errorf("create with %v: error = %v, want a 422 on the field", in, err)
		}
	}
	// A null on create is "no value": nothing to refuse.
	if _, err := ValidateMetadata(&movieType, fields, map[string]any{"genres": nil}, nil); err != nil {
		t.Errorf("null genres on create: %v", err)
	}
}

func TestF028B1_PatchOfLookupOwnedValues(t *testing.T) {
	fields := f028Movie(t)
	stored := map[string]any{"genres": []any{"Drama"}, "tmdb_rating": 7.4, "tmdb_vote_count": float64(1200), "director": "Old"}
	for name, tc := range map[string]struct {
		in      map[string]any
		wantErr string // the refused field, "" when accepted
		kept    bool   // the stored lookup values survive
	}{
		"a different genre is refused":      {in: map[string]any{"genres": []any{"Western"}}, wantErr: "metadata.genres"},
		"a different score is refused":      {in: map[string]any{"tmdb_rating": 9.0}, wantErr: "metadata.tmdb_rating"},
		"null removes it, so it is refused": {in: map[string]any{"genres": nil}, wantErr: "metadata.genres"},
		"the stored values are accepted":    {in: map[string]any{"genres": []any{"Drama"}, "tmdb_rating": 7.4, "tmdb_vote_count": 1200}, kept: true},
		"an omitted value is kept":          {in: map[string]any{"director": "New"}, kept: true},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := ValidateMetadata(&movieType, fields, tc.in, stored)
			if tc.wantErr != "" {
				if got := validationFields(err); len(got) != 1 || got[0] != tc.wantErr {
					t.Fatalf("error = %v, want a 422 on %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.kept {
				return
			}
			if !reflect.DeepEqual(out["genres"], []string{"Drama"}) || out["tmdb_rating"] != 7.4 || out["tmdb_vote_count"] != float64(1200) {
				t.Errorf("out = %#v, want the stored lookup values", out)
			}
		})
	}
}

func TestF028B1_LookupOwnedFieldsAreNotSortableOrFilterable(t *testing.T) {
	fields := f028Movie(t)
	defs := ListDefs{SortMode: SortManual, Fields: fields}
	all := []Item{
		mkItem(1, "Alpha", `{"genres":["Drama"],"tmdb_rating":7.4}`),
		mkItem(2, "Beta", `{"tmdb_rating":9.1}`),
	}
	if _, _, err := Query(all, &movie, defs, ListParams{Sort: "genres"}); !errors.Is(err, ErrInvalidSort) {
		t.Errorf("genres sort: err = %v, want ErrInvalidSort", err)
	}
	if _, _, err := Query(all, &movie, defs, ListParams{FilterField: "genres", FilterValue: "Drama"}); !errors.Is(err, ErrInvalidFilter) {
		t.Errorf("genres filter: err = %v, want ErrInvalidFilter (the filter is a later rule)", err)
	}
	// The score is an ordinary number field: an ascending sort works, missing scores last.
	page, _ := mustQuery(t, all, &movie, defs, ListParams{Sort: "tmdb_rating"})
	if got := nums(page); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("tmdb_rating sort = %v, want [1 2]", got)
	}
}
