package items

import (
	"strings"
	"unicode"

	"github.com/google/uuid"

	"golang.org/x/text/unicode/norm"
)

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

// TitleTaken reports whether one of titles has the title's key (F-026); the
// lookup create path uses it on the list's active titles.
func TitleTaken(titles []string, title string) bool {
	key := titleKey(title)
	for _, t := range titles {
		if titleKey(t) == key {
			return true
		}
	}
	return false
}

// titleTaken reports whether an item of rows other than skip has the title's key.
func titleTaken(rows []Item, title string, skip uuid.UUID) bool {
	key := titleKey(title)
	for _, it := range rows {
		if it.ID != skip && titleKey(it.Title) == key {
			return true
		}
	}
	return false
}
