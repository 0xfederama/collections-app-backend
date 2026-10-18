package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestF014B3_KindFieldLifecycle(t *testing.T) {
	e := newTreeEnv(t)
	kind := e.kind("movie")

	created := e.call(http.MethodPost, "/api/v1/kinds/movie/fields", `{"label":"Rating","type":"number"}`, etagOf("k", kind))
	wantStatus(t, created, http.StatusCreated)
	field := decode(t, created)
	if field["key"] != "rating" || created.Header().Get("ETag") != etagOf("kf", field) {
		t.Fatalf("created field = %v, ETag = %q", field, created.Header().Get("ETag"))
	}

	list := e.listOfKind("Movies", nil, "movie")
	item := e.newItem(list["id"].(string), `{"title":"The Matrix","metadata":{"rating":5}}`)
	preview := e.call(http.MethodGet, "/api/v1/kinds/movie/fields/rating", "", "")
	wantStatus(t, preview, http.StatusOK)
	if decode(t, preview)["populated_item_count"] != float64(1) {
		t.Fatalf("preview = %s", preview.Body)
	}

	renamed := e.call(http.MethodPatch, "/api/v1/kinds/movie/fields/rating", `{"label":"Personal rating"}`, etagOf("kf", field))
	wantStatus(t, renamed, http.StatusOK)
	field = decode(t, renamed)
	if field["label"] != "Personal rating" || renamed.Header().Get("ETag") != etagOf("kf", field) {
		t.Fatalf("renamed field = %v, ETag = %q", field, renamed.Header().Get("ETag"))
	}

	kind = e.kind("movie")
	reordered := e.call(http.MethodPut, "/api/v1/kinds/movie/fields/order", `{"keys":["rating"]}`, etagOf("k", kind))
	wantStatus(t, reordered, http.StatusNoContent)
	if reordered.Header().Get("ETag") == "" {
		t.Fatal("reorder response omitted ETag")
	}
	field["version"] = field["version"].(float64) + 1

	deleted := e.call(http.MethodDelete, "/api/v1/kinds/movie/fields/rating", "", etagOf("kf", field))
	wantStatus(t, deleted, http.StatusOK)
	if decode(t, deleted)["populated_item_count"] != float64(1) {
		t.Fatalf("delete = %s", deleted.Body)
	}
	fetched := e.fetch("items", item["id"].(string))
	metadata, ok := fetched["metadata"].(map[string]any)
	if !ok || metadata["rating"] != nil {
		t.Errorf("metadata after deletion = %#v", fetched["metadata"])
	}

	locked := e.call(http.MethodDelete, "/api/v1/kinds/movie/fields/release_year", "", `"kf-1"`)
	p := wantProblem(t, locked, http.StatusUnprocessableEntity, "validation_failed")
	if len(p.Errors) != 1 || p.Errors[0].Field != "field" {
		t.Errorf("locked field problem = %+v", p)
	}
}

func (e *treeEnv) kind(key string) obj {
	e.t.Helper()
	rec := e.call(http.MethodGet, "/api/v1/kinds", "", "")
	wantStatus(e.t, rec, http.StatusOK)
	var kinds []obj
	if err := json.Unmarshal(rec.Body.Bytes(), &kinds); err != nil {
		e.t.Fatal(err)
	}
	for _, kind := range kinds {
		if kind["key"] == key {
			return kind
		}
	}
	e.t.Fatalf("kind %q not found", key)
	return nil
}
