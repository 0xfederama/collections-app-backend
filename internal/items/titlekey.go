package items

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items/types"

	"golang.org/x/text/unicode/norm"
)

// TitleEntry is what the duplicate rule (F-026) reads from an item: its
// title and its stored metadata JSON.
type TitleEntry struct {
	Title    string
	Metadata []byte
}

// titleKey is the form titles are compared in for uniqueness within a list
// (F-026): trimmed, accents dropped (NFD without combining marks), case folded.
func titleKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.TrimSpace(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// providerKey is the item's provider identity ("tmdb:603", "openlibrary:OL1W"),
// or "" when the metadata has none.
func providerKey(meta []byte) string {
	var m struct {
		TMDB        json.Number `json:"tmdb_id"`
		OpenLibrary string      `json:"openlibrary_id"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &m) != nil {
		return ""
	}
	if m.TMDB != "" {
		if n, err := strconv.ParseInt(string(m.TMDB), 10, 64); err == nil {
			return "tmdb:" + strconv.FormatInt(n, 10)
		}
	}
	if s := strings.TrimSpace(m.OpenLibrary); s != "" {
		return "openlibrary:" + strings.ToUpper(s)
	}
	return ""
}

// idTyped reports whether items of the list type carry a provider id
// (movie and TV: tmdb_id; book: openlibrary_id).
func idTyped(typ *string) bool {
	return typ != nil && (*typ == types.Movie || *typ == types.TVShow || *typ == types.Book)
}

// sameEntry is the duplicate rule (F-026, user 2026-11-05): on a list whose type
// has a provider id, two items are the same only when both have that id and it
// is equal (titles never matter: remakes share titles, and an item without an
// id never clashes); on any other list, when their titles have equal keys.
func sameEntry(typ *string, a, b TitleEntry) bool {
	if idTyped(typ) {
		ka := providerKey(a.Metadata)
		return ka != "" && ka == providerKey(b.Metadata)
	}
	return titleKey(a.Title) == titleKey(b.Title)
}

// EntryTaken reports whether one of entries is the same as e (sameEntry) on a
// list of type typ; the lookup create path uses it on the list's active items.
func EntryTaken(typ *string, entries []TitleEntry, e TitleEntry) bool {
	for _, x := range entries {
		if sameEntry(typ, x, e) {
			return true
		}
	}
	return false
}

// entryTaken reports whether an item of rows other than skip is the same as e.
func entryTaken(typ *string, rows []Item, e TitleEntry, skip uuid.UUID) bool {
	for _, it := range rows {
		if it.ID != skip && sameEntry(typ, TitleEntry{Title: it.Title, Metadata: it.Metadata}, e) {
			return true
		}
	}
	return false
}
