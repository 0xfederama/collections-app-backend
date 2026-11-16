package items

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// F-028 B3 (D-163): the genre filter of a movie or TV list and the genres its menu offers.

// maxGenreFilter is the most genre values one filter takes; each is at most maxGenreName characters.
const (
	maxGenreFilter = 10
	maxGenreName   = 100
)

// movieOrTV reports whether the list type is movie or TV, the types with rated and genred items.
func movieOrTV(typ *string) bool {
	return typ != nil && (*typ == types.Movie || *typ == types.TVShow)
}

// genreFilter validates the genre values and returns them trimmed and lower-cased, ready for an exact,
// case-insensitive match. No values means no genre filter.
func genreFilter(typ *string, in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if !movieOrTV(typ) || len(in) > maxGenreFilter {
		return nil, ErrInvalidGenre
	}
	out := make([]string, 0, len(in))
	for _, g := range in {
		s := strings.TrimSpace(g)
		if n := utf8.RuneCountInString(s); n < 1 || n > maxGenreName {
			return nil, ErrInvalidGenre
		}
		out = append(out, strings.ToLower(s))
	}
	return out, nil
}

// matchesGenres keeps an item with any of the wanted genres (want is genreFilter's output). With no
// wanted genres every item matches.
func matchesGenres(v any, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, g := range genresOf(v) {
		if slices.Contains(want, strings.ToLower(g)) {
			return true
		}
	}
	return false
}

// genresOf reads the stored genres of an item: the non-blank, trimmed names, in stored order.
func genresOf(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// GenreCount is one genre of a list's active items and how many of them carry it.
type GenreCount struct {
	Name  string
	Count int
}

// GenreCounts returns the genres of the items of a movie or TV list (D-163). An item counts once per
// genre; names match case-insensitively after trimming, and the spelling is the first one seen in the
// list's order. The result is sorted by name, case-insensitively. Any other type has none.
func GenreCounts(all []Item, typ *string) ([]GenreCount, error) {
	out := make([]GenreCount, 0)
	if !movieOrTV(typ) {
		return out, nil
	}
	index := map[string]int{}
	for _, it := range all {
		meta, err := decodeMetadata(it.Metadata)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, g := range genresOf(meta["genres"]) {
			key := strings.ToLower(g)
			if seen[key] {
				continue
			}
			seen[key] = true
			if i, ok := index[key]; ok {
				out[i].Count++
				continue
			}
			index[key] = len(out)
			out = append(out, GenreCount{Name: g, Count: 1})
		}
	}
	slices.SortFunc(out, func(a, b GenreCount) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}
