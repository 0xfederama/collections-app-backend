// Package stores owns the fixed list of online stores a `stores` field can
// name (F-016, D-096). Metadata and workspace settings keep provider keys only;
// the links are built by the web app. Nothing in this package does I/O.
package stores

import (
	"fmt"
	"slices"
)

// Provider is one selectable store: a stable key and its display name.
type Provider struct {
	Key  string
	Name string
}

// providers is the fixed order the API and the UI show. Keys are stored in
// metadata and workspace settings, so renaming one is a data migration.
var providers = []Provider{
	{Key: "amazon", Name: "Amazon"},
	{Key: "bookshop", Name: "Bookshop.org"},
	{Key: "apple_books", Name: "Apple Books"},
	{Key: "google_play_books", Name: "Google Play Books"},
	{Key: "kobo", Name: "Kobo"},
}

// Providers returns the providers in their fixed order (a copy).
func Providers() []Provider { return slices.Clone(providers) }

// Valid reports whether key is one of the providers.
func Valid(key string) bool {
	return slices.ContainsFunc(providers, func(p Provider) bool { return p.Key == key })
}

// ValidateKeys checks a list of provider keys: each must be known and none may
// repeat. The error message names the first problem and is shown to the client.
func ValidateKeys(keys []string) error {
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !Valid(key) {
			return fmt.Errorf("unknown store %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate store %q", key)
		}
		seen[key] = true
	}
	return nil
}
