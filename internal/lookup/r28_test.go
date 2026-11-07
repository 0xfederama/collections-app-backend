package lookup

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/items"
)

func TestR28_ApplyCreateDropsOverlongProviderField(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "756",
		Title: "Fantasia", OriginalTitle: "Fantasia",
		CreateInput: &items.CreateInput{Title: "Fantasia"},
		Fields:      map[string]any{"release_year": 1940, "director": strings.Repeat("Director Name, ", 12)},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	metadata := decodeMetadata(t, it.Metadata)
	if _, has := metadata["director"]; has || metadata["release_year"] != float64(1940) || metadata["tmdb_id"] != float64(756) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestR28_ValidateImportDropsOnlyOffendingFields(t *testing.T) {
	typ := "movie"
	fields, _ := typeFields(&typ)
	long := strings.Repeat("x", 200)
	in := map[string]any{"director": long, "release_year": 1999}

	out, dropped, err := validateImport(nil, nil, map[string]any{}, nil, nil)
	if err != nil || len(out) != 0 || len(dropped) != 0 {
		t.Fatalf("empty: %v %v %v", out, dropped, err)
	}
	out, dropped, err = validateImport(&typ, fields, in, nil, nil)
	if err != nil || !reflect.DeepEqual(dropped, []string{"director"}) || len(out) != 1 {
		t.Fatalf("out=%v dropped=%v err=%v", out, dropped, err)
	}
	// The stored value stays when the new one is invalid.
	out, _, err = validateImport(&typ, fields, in, map[string]any{"director": "Old"}, nil)
	if err != nil || out["director"] != "Old" {
		t.Fatalf("fallback: out=%v err=%v", out, err)
	}
	// A key the client sent is never dropped.
	if _, _, err = validateImport(&typ, fields, in, nil, map[string]bool{"director": true}); err == nil {
		t.Fatal("client field was dropped")
	}
}
