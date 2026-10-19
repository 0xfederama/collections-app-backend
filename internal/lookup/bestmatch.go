package lookup

import (
	"strings"
	"unicode"
)

// pickBest returns the index of the Open Library result an automatic lookup
// (no selected id) should use. Only results whose normalised title equals the
// normalised query are candidates; among them the one with the most of author
// and page count wins, ties going to the earliest upstream result. With no
// exact title match (or an ISBN query) it returns 0, the upstream order.
func pickBest(query string, matches []Suggestion) int {
	want := normalizeTitle(query)
	if want == "" {
		return 0
	}
	best, bestScore := 0, -1
	for i, m := range matches {
		if normalizeTitle(m.Title) != want {
			continue
		}
		score := 0
		if strings.TrimSpace(m.Authors) != "" {
			score++
		}
		if m.Pages > 0 {
			score++
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// diacritics maps the common Latin accented letters to their base letter
// (stdlib only, no new dependency).
var diacritics = map[rune]rune{}

func init() {
	for base, accented := range map[rune]string{
		'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ďđ", 'e': "èéêëēĕėęě", 'g': "ĝğġģ",
		'h': "ĥħ", 'i': "ìíîïĩīĭįı", 'j': "ĵ", 'k': "ķ", 'l': "ĺļľŀł", 'n': "ñńņňŉ",
		'o': "òóôõöøōŏő", 'r': "ŕŗř", 's': "śŝşš", 't': "ţťŧ", 'u': "ùúûüũūŭůűų",
		'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
	} {
		for _, r := range accented {
			diacritics[r] = base
		}
	}
}

// normalizeTitle lowercases, strips diacritics and punctuation, and collapses
// whitespace.
func normalizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if base, ok := diacritics[r]; ok {
			r = base
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case unicode.IsSpace(r):
			space = true
		case unicode.Is(unicode.Mn, r):
			// combining mark: dropped
		default:
			// punctuation is dropped without splitting words ("l'altro" -> "laltro")
		}
	}
	return b.String()
}
