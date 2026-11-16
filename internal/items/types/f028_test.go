package types_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F-028 (D-162): the imported score, vote count and genres of movie and TV items.

func TestF028B1_MovieAndTVHaveTheImportedFieldsBookDoesNot(t *testing.T) {
	want := map[string]string{"tmdb_rating": "number", "tmdb_vote_count": "number", "genres": "text_list"}
	for _, key := range []string{types.Movie, types.TVShow} {
		def, ok := types.Lookup(key)
		if !ok {
			t.Fatalf("%s is not a built-in type", key)
		}
		got := map[string]string{}
		for _, f := range def.Fields {
			if _, wanted := want[f.Key]; wanted {
				got[f.Key] = f.Type
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields = %v, want %v", key, got, want)
		}
	}
	book, _ := types.Lookup(types.Book)
	for _, f := range book.Fields {
		if _, wanted := want[f.Key]; wanted {
			t.Errorf("book has %s: genres and score are not imported for books (D-162)", f.Key)
		}
	}
}

func TestF028B1_TextListRule(t *testing.T) {
	fields := []types.Field{{Key: "genres", Type: "text_list"}}
	long := strings.Repeat("a", 101)
	for name, tc := range map[string]struct {
		in      any
		want    any
		wantErr string // the field of the error (under metadata.), "" for none
	}{
		"trimmed, in order":         {in: []any{"  Drama ", "Science Fiction"}, want: []string{"Drama", "Science Fiction"}},
		"a typed Go list":           {in: []string{"Drama"}, want: []string{"Drama"}},
		"ten entries is the limit":  {in: namesAny(10), want: namesOf(10)},
		"empty list":                {in: []any{}, wantErr: "genres"},
		"eleven entries":            {in: namesAny(11), wantErr: "genres"},
		"not an array":              {in: "Drama", wantErr: "genres"},
		"entry is not a string":     {in: []any{3}, wantErr: "genres[0]"},
		"blank entry":               {in: []any{"   "}, wantErr: "genres[0]"},
		"entry over 100 characters": {in: []any{"ok", long}, wantErr: "genres[1]"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := types.Validate(fields, map[string]any{"genres": tc.in})
			if tc.wantErr != "" {
				var v *apperr.ValidationError
				if !errors.As(err, &v) || len(v.Errors) == 0 || v.Errors[0].Field != "metadata."+tc.wantErr {
					t.Fatalf("error = %v, want a validation error on metadata.%s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["genres"], tc.want) {
				t.Errorf("genres = %#v, want %#v", got["genres"], tc.want)
			}
		})
	}
}

// namesOf returns n distinct genre names.
func namesOf(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "Genre " + string(rune('A'+i))
	}
	return out
}

// namesAny is namesOf as a decoded JSON array.
func namesAny(n int) []any {
	names := namesOf(n)
	out := make([]any, n)
	for i, s := range names {
		out[i] = s
	}
	return out
}
