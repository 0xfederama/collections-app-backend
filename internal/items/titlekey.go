package items

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"

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

// sameEntry is the duplicate rule (F-026, user 2026-11-05): two items that both
// have a provider identity are the same when the identity is equal, whatever
// their titles (remakes may share a title, a refresh never changes the id);
// otherwise they are the same when their titles have equal keys.
func sameEntry(a, b TitleEntry) bool {
	ka, kb := providerKey(a.Metadata), providerKey(b.Metadata)
	if ka != "" && kb != "" {
		return ka == kb
	}
	return titleKey(a.Title) == titleKey(b.Title)
}

// EntryTaken reports whether one of entries is the same as e (sameEntry); the
// lookup create path uses it on the list's active items.
func EntryTaken(entries []TitleEntry, e TitleEntry) bool {
	for _, x := range entries {
		if sameEntry(x, e) {
			return true
		}
	}
	return false
}

// entryTaken reports whether an item of rows other than skip is the same as e.
func entryTaken(rows []Item, e TitleEntry, skip uuid.UUID) bool {
	for _, it := range rows {
		if it.ID != skip && sameEntry(TitleEntry{Title: it.Title, Metadata: it.Metadata}, e) {
			return true
		}
	}
	return false
}
