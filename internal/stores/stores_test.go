package stores_test

import (
	"slices"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/stores"
)

func TestF016B4_ProvidersAreFixedAndOrdered(t *testing.T) {
	want := []stores.Provider{
		{Key: "openlibrary", Name: "Open Library"},
		{Key: "bookshop", Name: "Bookshop.org"},
		{Key: "amazon", Name: "Amazon"},
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
	if stores.Providers()[0].Key != "openlibrary" {
		t.Fatal("Providers() shares its backing array")
	}
}

func TestF016B4_Valid(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"openlibrary", true},
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
		{"several known", []string{"kobo", "amazon", "openlibrary"}, false},
		{"all six", []string{"openlibrary", "bookshop", "amazon", "apple_books", "google_play_books", "kobo"}, false},
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
			name: "open library by work id", provider: "openlibrary",
			in:   stores.LinkInput{ISBN: "9780140449136", Title: "The Odyssey", OpenLibraryID: "OL2071383W"},
			want: "https://openlibrary.org/works/OL2071383W",
		},
		{
			name: "open library by isbn when no work id", provider: "openlibrary",
			in:   stores.LinkInput{ISBN: "978-0-14-044913-6", Title: "The Odyssey"},
			want: "https://openlibrary.org/isbn/9780140449136",
		},
		{
			name: "open library title search without isbn or id", provider: "openlibrary",
			in:   stores.LinkInput{Title: "Dune & Co"},
			want: "https://openlibrary.org/search?q=Dune+%26+Co",
		},
		{
			name: "open library escapes the work id", provider: "openlibrary",
			in:   stores.LinkInput{OpenLibraryID: "OL 1/W?x"},
			want: "https://openlibrary.org/works/OL%201%2FW%3Fx",
		},
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
			name: "amazon by isbn", provider: "amazon",
			in:   stores.LinkInput{ISBN: "9780441172719"},
			want: "https://www.amazon.com/s?k=9780441172719",
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
			name: "nothing to search for on open library", provider: "openlibrary",
			in:   stores.LinkInput{},
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
		link := stores.Link(p.Key, stores.LinkInput{ISBN: "9780441172719", Title: "Dune", OpenLibraryID: "OL1W"})
		if len(link) < len("https://") || link[:len("https://")] != "https://" {
			t.Errorf("Link(%q) = %q, want https", p.Key, link)
		}
	}
}
