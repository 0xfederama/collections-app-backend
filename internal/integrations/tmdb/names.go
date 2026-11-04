package tmdb

import "unicode"

// translation is one entry of TMDB's append_to_response=translations list.
// Movie titles sit in data.title and TV titles in data.name.
type translation struct {
	ISO639  string `json:"iso_639_1"`
	ISO3166 string `json:"iso_3166_1"`
	Data    struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"data"`
}

// isLatin reports whether s has at least one letter and every letter is in the
// Latin script. Digits, punctuation, spaces and combining marks are not letters,
// so they never disqualify a name ("2001: A Space Odyssey", "Ça", Vietnamese).
func isLatin(s string) bool {
	hasLetter := false
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.Is(unicode.Latin, r) {
			return false
		}
		hasLetter = true
	}
	return hasLetter
}

// chooseTitle returns the movie or TV title the app shows: the localized value,
// then the English translations (US first), then the Latin original. Only when
// none of those is Latin does it keep the non-Latin original, so a title is never
// lost.
func chooseTitle(localized string, english []string, original string) string {
	candidates := append([]string{localized}, english...)
	candidates = append(candidates, original)
	for _, c := range candidates {
		if isLatin(c) {
			return c
		}
	}
	return firstNonEmpty(append([]string{original, localized}, english...)...)
}

// chooseName returns a person's name in Latin script: the name itself when it is
// Latin, else the first Latin also_known_as entry, else the name as TMDB has it.
func chooseName(name string, alsoKnownAs []string) string {
	if isLatin(name) {
		return name
	}
	for _, alias := range alsoKnownAs {
		if isLatin(alias) {
			return alias
		}
	}
	return name
}

// englishTitles returns the en translations for typ, the US variant first, then
// the other English variants. Empty values are skipped.
func englishTitles(typ string, translations []translation) []string {
	var us, other []string
	for _, t := range translations {
		if t.ISO639 != "en" {
			continue
		}
		value := t.Data.Title
		if typ == "tv_show" {
			value = t.Data.Name
		}
		if value == "" {
			continue
		}
		if t.ISO3166 == "US" {
			us = append(us, value)
		} else {
			other = append(other, value)
		}
	}
	return append(us, other...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
