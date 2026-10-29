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
