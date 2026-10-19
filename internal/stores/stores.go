// Package stores owns the fixed list of online stores a `stores` field can
// name (F-016, D-096). Metadata keeps provider keys only; links are built here,
// at read time, from an item's ISBN, title or Open Library id. Nothing in this
// package does I/O.
package stores

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Provider is one selectable store: a stable key and its display name.
type Provider struct {
	Key  string
	Name string
}

// providers is the fixed order the API and the UI show. Keys are stored in
// metadata and workspace settings, so renaming one is a data migration.
var providers = []Provider{
	{Key: "openlibrary", Name: "Open Library"},
	{Key: "bookshop", Name: "Bookshop.org"},
	{Key: "amazon", Name: "Amazon"},
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

// LinkInput is what a link is built from. Empty fields are ignored. Country is
// the workspace's ISO 3166-1 alpha-2 code (the `streaming_country_code` setting)
// and picks regional storefronts; empty or unknown means the global site.
type LinkInput struct {
	ISBN          string
	Title         string
	OpenLibraryID string
	Country       string
}

// amazonDomains maps a country to its Amazon storefront; any other country uses
// amazon.com. Keep it identical to AMAZON_DOMAINS in frontend-web
// (src/features/items/links.ts).
var amazonDomains = map[string]string{
	"IT": "amazon.it", "DE": "amazon.de", "FR": "amazon.fr", "ES": "amazon.es",
	"GB": "amazon.co.uk", "NL": "amazon.nl", "SE": "amazon.se", "PL": "amazon.pl",
	"BE": "amazon.com.be", "IE": "amazon.ie", "CA": "amazon.ca", "MX": "amazon.com.mx",
	"BR": "amazon.com.br", "AU": "amazon.com.au", "JP": "amazon.co.jp", "IN": "amazon.in",
	"TR": "amazon.com.tr", "AE": "amazon.ae", "SA": "amazon.sa", "SG": "amazon.sg",
	"EG": "amazon.eg",
}

// AmazonDomain returns the Amazon storefront host for a country code (any case).
func AmazonDomain(country string) string {
	if d, ok := amazonDomains[strings.ToUpper(strings.TrimSpace(country))]; ok {
		return d
	}
	return "amazon.com"
}

// Link returns the public URL of the provider for the book described by in, or
// "" when the provider is unknown or there is nothing to search for. An ISBN
// (hyphens and spaces removed) is preferred; the title is the fallback search.
// Open Library also uses its own work id first. Amazon, Bookshop.org (UK) and Google Play follow the country; Open Library, Apple Books and Kobo stay global (no stable regional search URL could be verified). Every value is URL-escaped and
// the scheme is always https.
func Link(provider string, in LinkInput) string {
	isbn := cleanISBN(in.ISBN)
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	query := isbn
	if query == "" {
		query = strings.TrimSpace(in.Title)
	}
	switch provider {
	case "openlibrary":
		switch {
		case strings.TrimSpace(in.OpenLibraryID) != "":
			return "https://openlibrary.org/works/" + url.PathEscape(strings.TrimSpace(in.OpenLibraryID))
		case isbn != "":
			return "https://openlibrary.org/isbn/" + url.PathEscape(isbn)
		case query != "":
			return "https://openlibrary.org/search?q=" + url.QueryEscape(query)
		}
		return ""
	}
	if query == "" {
		return ""
	}
	switch provider {
	case "bookshop":
		// Bookshop.org has a US and a UK site only.
		host := "bookshop.org"
		if country == "GB" {
			host = "uk.bookshop.org"
		}
		return "https://" + host + "/search?keywords=" + url.QueryEscape(query)
	case "amazon":
		return "https://www." + AmazonDomain(country) + "/s?k=" + url.QueryEscape(query)
	case "apple_books":
		return "https://books.apple.com/search?term=" + url.QueryEscape(query)
	case "google_play_books":
		link := "https://play.google.com/store/search?q=" + url.QueryEscape(query) + "&c=books"
		if len(country) == 2 {
			link += "&gl=" + country
		}
		return link
	case "kobo":
		return "https://www.kobo.com/search?query=" + url.QueryEscape(query)
	}
	return ""
}

// cleanISBN drops the separators people type into an ISBN.
func cleanISBN(s string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s))
}
