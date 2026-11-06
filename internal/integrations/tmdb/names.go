package tmdb

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"unicode"
)

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

// directorNames returns the distinct directors of a movie in Latin script. Only a
// non-Latin credit name costs a /person call, and only the first maxPersonLookups
// of them, so one Details call stays bounded. Any /person failure (429, timeout,
// 5xx, 404, bad body) keeps the credit name and logs a warning: the name is an
// enrichment, never a reason to fail the whole Details.
func (c *Client) directorNames(ctx context.Context, crew []crewMember) []string {
	names := make([]string, 0)
	lookups := 0
	for _, person := range crew {
		if person.Job != "Director" || person.Name == "" {
			continue
		}
		name := person.Name
		if !isLatin(name) && person.ID > 0 && lookups < maxPersonLookups {
			lookups++
			name = c.latinDirectorName(ctx, person)
		}
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// latinDirectorName returns the Latin name for one director, or the credit name
// when /person cannot provide one.
func (c *Client) latinDirectorName(ctx context.Context, person crewMember) string {
	var detail struct {
		ID          int      `json:"id"`
		AlsoKnownAs []string `json:"also_known_as"`
	}
	if err := c.getJSON(ctx, "/person/"+strconv.Itoa(person.ID), url.Values{}, &detail); err != nil {
		slog.WarnContext(ctx, "TMDB person lookup failed; keeping the credit name", "person_id", person.ID, "err", err)
		return person.Name
	}
	if detail.ID != person.ID {
		slog.WarnContext(ctx, "TMDB person lookup returned another person; keeping the credit name", "person_id", person.ID)
		return person.Name
	}
	return chooseName(person.Name, detail.AlsoKnownAs)
}
