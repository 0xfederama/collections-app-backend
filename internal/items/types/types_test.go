package types_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

func TestF014B2_ValidateDefinitionFields(t *testing.T) {
	fields := []types.Field{
		{Key: "notes", Type: "text"},
		{Key: "rating", Type: "number"},
		{Key: "published", Type: "date"},
		{Key: "owned", Type: "boolean"},
		{Key: "website", Type: "link"},
	}
	got, err := types.Validate(fields, map[string]any{
		"notes": "  A fine game  ", "rating": 4.5, "published": "2026-10-18",
		"owned": true, "website": " https://example.com/game ",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"notes": "A fine game", "rating": 4.5, "published": "2026-10-18", "owned": true, "website": "https://example.com/game"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Validate() = %#v, want %#v", got, want)
	}
}

func TestF014B2_ValidateDefinitionFieldErrors(t *testing.T) {
	fields := []types.Field{{Key: "text", Type: "text"}, {Key: "number", Type: "number"}, {Key: "date", Type: "date"}, {Key: "boolean", Type: "boolean"}, {Key: "link", Type: "link"}}
	for _, tt := range []struct {
		name string
		in   map[string]any
		path string
	}{
		{"blank text", map[string]any{"text": "  "}, "metadata.text"},
		{"long text", map[string]any{"text": strings.Repeat("a", 101)}, "metadata.text"},
		{"non-finite number", map[string]any{"number": math.Inf(1)}, "metadata.number"},
		{"bad date", map[string]any{"date": "18-10-2026"}, "metadata.date"},
		{"bad boolean", map[string]any{"boolean": "true"}, "metadata.boolean"},
		{"bad link", map[string]any{"link": "ftp://example.com"}, "metadata.link"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := types.Validate(fields, tt.in)
			var validation *apperr.ValidationError
			if !errors.As(err, &validation) || len(validation.Errors) != 1 || validation.Errors[0].Field != tt.path {
				t.Fatalf("Validate() error = %v, want %s", err, tt.path)
			}
		})
	}
}

func TestF014B2_ValidateRejectsUnknownDefinitionField(t *testing.T) {
	_, err := types.Validate([]types.Field{{Key: "rating", Type: "number"}}, map[string]any{"unknown": 1, "rating": "bad"})
	if err == nil || !strings.Contains(err.Error(), "metadata.unknown") {
		t.Fatalf("Validate() error = %v, want unknown metadata field", err)
	}
}

func TestF014B2_ValidateKeepsStreamingAndTMDBRules(t *testing.T) {
	fields := []types.Field{{Key: "streaming", Type: "streaming"}, {Key: "tmdb_id", Type: "number"}, {Key: "lookup_disabled", Type: "boolean"}, {Key: "imdb_url", Type: "link"}}
	got, err := types.Validate(fields, map[string]any{
		"streaming":       map[string]any{"country": "IT", "platforms": []any{"Netflix"}, "paid_platforms": []any{"netflix"}},
		"tmdb_id":         603,
		"lookup_disabled": false,
		"imdb_url":        "https://www.imdb.com/title/tt0133093/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["streaming"].(map[string]any)["paid_platforms"].([]string)[0] != "Netflix" {
		t.Fatalf("paid platform was not normalized: %#v", got["streaming"])
	}
	for _, in := range []map[string]any{
		{"streaming": map[string]any{"country": "it"}},
		{"tmdb_id": 0},
		{"lookup_disabled": "false"},
		{"imdb_url": "https://example.com/title/tt0133093/"},
	} {
		if _, err := types.Validate(fields, in); err == nil {
			t.Fatalf("Validate(%#v) succeeded", in)
		}
	}
}

func TestStreamingLinkValidation(t *testing.T) {
	fields := []types.Field{{Key: "streaming", Type: "streaming"}}
	ok := "https://www.justwatch.com/it/movie/the-matrix"
	got, err := types.Validate(fields, map[string]any{"streaming": map[string]any{"country": "IT", "link": ok}})
	if err != nil || got["streaming"].(map[string]any)["link"] != ok {
		t.Fatalf("valid link: %v, %v", got, err)
	}
	for _, link := range []any{"http://www.justwatch.com/x", "https://evil.example/x", "https://www.justwatch.com.evil.test/", 5} {
		if _, err := types.Validate(fields, map[string]any{"streaming": map[string]any{"country": "IT", "link": link}}); err == nil {
			t.Errorf("link %v accepted", link)
		}
	}
}

func TestF016B4_ValidateStoresField(t *testing.T) {
	fields := []types.Field{{Key: "stores", Type: "stores"}}
	for _, tt := range []struct {
		name  string
		value any
		want  []string
		path  string
	}{
		{"unique known keys keep their order", []any{"kobo", "bookshop"}, []string{"kobo", "bookshop"}, ""},
		{"open library is not a store", []any{"openlibrary"}, nil, "metadata.stores"},
		{"empty array", []any{}, []string{}, ""},
		{"unknown key", []any{"kobo", "audible"}, nil, "metadata.stores"},
		{"duplicate key", []any{"kobo", "kobo"}, nil, "metadata.stores"},
		{"not an array", "kobo", nil, "metadata.stores"},
		{"non-string entry", []any{"kobo", 3}, nil, "metadata.stores[1]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := types.Validate(fields, map[string]any{"stores": tt.value})
			if tt.path == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if !reflect.DeepEqual(got["stores"], tt.want) {
					t.Fatalf("stores = %#v, want %#v", got["stores"], tt.want)
				}
				return
			}
			var validation *apperr.ValidationError
			if !errors.As(err, &validation) || len(validation.Errors) != 1 || validation.Errors[0].Field != tt.path {
				t.Fatalf("Validate error = %v, want a 422 on %s", err, tt.path)
			}
		})
	}
	if _, err := types.Validate(fields, map[string]any{"stores": nil}); err != nil {
		t.Fatalf("null stores should be omitted: %v", err)
	}
}
