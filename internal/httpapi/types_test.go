package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestF023_ListTypesServesTheBuiltinRegistry(t *testing.T) {
	e := newTreeEnv(t)
	rec := e.call(http.MethodGet, "/api/v1/types", "", "")
	wantStatus(t, rec, http.StatusOK)
	var got []obj
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, typ := range got {
		keys = append(keys, typ["key"].(string))
		for _, banned := range []string{"is_locked", "version"} {
			if _, ok := typ[banned]; ok {
				t.Errorf("type %v still carries %s", typ["key"], banned)
			}
		}
	}
	if want := "movie,tv_show,book,vehicle,wishlist"; strings.Join(keys, ",") != want {
		t.Fatalf("types = %v, want %s", keys, want)
	}
	movie := got[0]
	first := movie["fields"].([]any)[0].(map[string]any)
	if movie["default_sort_field"] != "release_year" || first["key"] != "release_year" || first["position"] != float64(0) {
		t.Errorf("movie = %v", movie)
	}
	for _, path := range []string{"/api/v1/types/movie/fields", "/api/v1/types/movie/fields/order", "/api/v1/types/movie/fields/director"} {
		wantProblem(t, e.call(http.MethodPost, path, `{"label":"Rating","type":"number"}`, `"k-1"`), http.StatusNotFound, "not_found")
	}
}

func (e *treeEnv) typ(key string) obj {
	e.t.Helper()
	rec := e.call(http.MethodGet, "/api/v1/types", "", "")
	wantStatus(e.t, rec, http.StatusOK)
	var types []obj
	if err := json.Unmarshal(rec.Body.Bytes(), &types); err != nil {
		e.t.Fatal(err)
	}
	for _, typ := range types {
		if typ["key"] == key {
			return typ
		}
	}
	e.t.Fatalf("type %q not found", key)
	return nil
}
