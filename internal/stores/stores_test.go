package stores_test

import (
	"slices"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/stores"
)

func TestF016B4_ProvidersAreFixedAndOrdered(t *testing.T) {
	want := []stores.Provider{
		{Key: "amazon", Name: "Amazon"},
		{Key: "bookshop", Name: "Bookshop.org"},
		{Key: "apple_books", Name: "Apple Books"},
		{Key: "google_play_books", Name: "Google Play Books"},
		{Key: "kobo", Name: "Kobo"},
	}
	got := stores.Providers()
	if !slices.Equal(got, want) {
		t.Fatalf("Providers() = %v, want %v", got, want)
	}
	// The returned slice is a copy: callers cannot reorder the list.
	got[0].Key = "changed"
	if stores.Providers()[0].Key != "amazon" {
		t.Fatal("Providers() shares its backing array")
	}
}

func TestF016B4_Valid(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"amazon", true},
		{"openlibrary", false},
		{"google_play_books", true},
		{"kobo", true},
		{"", false},
		{"Kobo", false},
		{"google-play-books", false},
		{"audible", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := stores.Valid(tt.key); got != tt.want {
				t.Errorf("Valid(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestF016B4_ValidateKeys(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string
		wantErr bool
	}{
		{"none", nil, false},
		{"several known", []string{"kobo", "amazon", "bookshop"}, false},
		{"open library is not a store", []string{"openlibrary"}, true},
		{"all five", []string{"amazon", "bookshop", "apple_books", "google_play_books", "kobo"}, false},
		{"unknown", []string{"kobo", "audible"}, true},
		{"duplicate", []string{"kobo", "kobo"}, true},
		{"empty string", []string{""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stores.ValidateKeys(tt.keys)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateKeys(%v) error = %v, wantErr %v", tt.keys, err, tt.wantErr)
			}
		})
	}
}

func TestF016B4_Link(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		in       stores.LinkInput
		want     string
	}{
		{
			name: "bookshop by isbn", provider: "bookshop",
			in:   stores.LinkInput{ISBN: "9780441172719", Title: "Dune"},
			want: "https://bookshop.org/search?keywords=9780441172719",
		},
		{
			name: "bookshop by title when no isbn", provider: "bookshop",
			in:   stores.LinkInput{Title: "Dune & Co"},
			want: "https://bookshop.org/search?keywords=Dune+%26+Co",
		},
		{
			name: "amazon without a title falls back to the isbn", provider: "amazon",
			in:   stores.LinkInput{ISBN: "9780441172719"},
			want: "https://www.amazon.com/s?k=9780441172719",
		},
		{
			name: "amazon follows the country", provider: "amazon",
			in:   stores.LinkInput{Title: "Dune", ISBN: "9780441172719", Country: "IT"},
			want: "https://www.amazon.it/s?k=Dune",
		},
		{
			name: "amazon searches by title even with an isbn", provider: "amazon",
			in:   stores.LinkInput{Title: "Il vecchio e il mare", ISBN: "9788804325963"},
			want: "https://www.amazon.com/s?k=Il+vecchio+e+il+mare",
		},
		{
			name: "amazon unknown country falls back to .com", provider: "amazon",
			in:   stores.LinkInput{Title: "Dune", Country: "ZZ"},
			want: "https://www.amazon.com/s?k=Dune",
		},
		{
			name: "bookshop uses the UK site for GB", provider: "bookshop",
			in:   stores.LinkInput{Title: "Dune", Country: "gb"},
			want: "https://uk.bookshop.org/search?keywords=Dune",
		},
		{
			name: "bookshop stays global for other countries", provider: "bookshop",
			in:   stores.LinkInput{Title: "Dune", Country: "IT"},
			want: "https://bookshop.org/search?keywords=Dune",
		},
		{
			name: "google play books gets the country", provider: "google_play_books",
			in:   stores.LinkInput{Title: "Dune", Country: "IT"},
			want: "https://play.google.com/store/search?q=Dune&c=books&gl=IT",
		},
		{
			name: "kobo and apple stay global with a country", provider: "kobo",
			in:   stores.LinkInput{Title: "Dune", Country: "IT"},
			want: "https://www.kobo.com/search?query=Dune",
		},
		{
			name: "amazon title is trimmed", provider: "amazon",
			in:   stores.LinkInput{Title: "  Dune  "},
			want: "https://www.amazon.com/s?k=Dune",
		},
		{
			name: "apple books by title", provider: "apple_books",
			in:   stores.LinkInput{Title: "Il nome della rosa"},
			want: "https://books.apple.com/search?term=Il+nome+della+rosa",
		},
		{
			name: "google play books keeps the books filter", provider: "google_play_books",
			in:   stores.LinkInput{ISBN: "9780441172719", Title: "Dune"},
			want: "https://play.google.com/store/search?q=9780441172719&c=books",
		},
		{
			name: "kobo by title", provider: "kobo",
			in:   stores.LinkInput{Title: "Dune"},
			want: "https://www.kobo.com/search?query=Dune",
		},
		{
			name: "nothing to search for", provider: "kobo",
			in:   stores.LinkInput{ISBN: " - ", Title: "   "},
			want: "",
		},
		{
			name: "open library is no longer a store", provider: "openlibrary",
			in:   stores.LinkInput{ISBN: "9780441172719", Title: "Dune"},
			want: "",
		},
		{
			name: "unknown provider", provider: "audible",
			in:   stores.LinkInput{ISBN: "9780441172719"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stores.Link(tt.provider, tt.in)
			if got != tt.want {
				t.Errorf("Link(%q, %+v) = %q, want %q", tt.provider, tt.in, got, tt.want)
			}
		})
	}
}

func TestF016B4_LinksAreHTTPS(t *testing.T) {
	for _, p := range stores.Providers() {
		link := stores.Link(p.Key, stores.LinkInput{ISBN: "9780441172719", Title: "Dune"})
		if len(link) < len("https://") || link[:len("https://")] != "https://" {
			t.Errorf("Link(%q) = %q, want https", p.Key, link)
		}
	}
}

func TestAmazonDomain(t *testing.T) {
	tests := map[string]string{
		"IT": "amazon.it", "DE": "amazon.de", "FR": "amazon.fr", "ES": "amazon.es", "GB": "amazon.co.uk",
		"NL": "amazon.nl", "SE": "amazon.se", "PL": "amazon.pl", "BE": "amazon.com.be", "IE": "amazon.ie",
		"CA": "amazon.ca", "MX": "amazon.com.mx", "BR": "amazon.com.br", "AU": "amazon.com.au",
		"JP": "amazon.co.jp", "IN": "amazon.in", "TR": "amazon.com.tr", "AE": "amazon.ae",
		"SA": "amazon.sa", "SG": "amazon.sg", "EG": "amazon.eg",
		"us": "amazon.com", "US": "amazon.com", "CH": "amazon.com", "": "amazon.com", " it ": "amazon.it",
	}
	for country, want := range tests {
		if got := stores.AmazonDomain(country); got != want {
			t.Errorf("AmazonDomain(%q) = %q, want %q", country, got, want)
		}
	}
}
