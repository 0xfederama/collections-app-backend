// Package etag formats and parses the typed ETags of API.md §3 ("f-7", "i-12", …).
package etag

import (
	"strconv"
	"strings"
)

// Prefixes per resource (API.md §3).
const (
	Folder   = "f"
	List     = "l"
	Item     = "i"
	Content  = "c"
	Settings = "s"
	Kind     = "k"
	Field    = "kf"
)

// Format returns the quoted ETag value, e.g. Format(Item, 7) == `"i-7"`.
func Format(prefix string, version int) string {
	return `"` + prefix + "-" + strconv.Itoa(version) + `"`
}

// Parse extracts N from a header value of exactly the form `"<prefix>-<N>"`.
// Weak validators, lists, `*`, other prefixes, signs and non-canonical numbers
// (leading zeros, overflow) all report false.
func Parse(header, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(header, `"`+prefix+"-")
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutSuffix(rest, `"`)
	if !ok || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return n, true
}
