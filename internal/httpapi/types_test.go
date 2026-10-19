package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestF014B3_TypeFieldLifecycle(t *testing.T) {
	e := newTreeEnv(t)
	typ := e.typ("movie")

	created := e.call(http.MethodPost, "/api/v1/types/movie/fields", `{"label":"Rating","type":"number"}`, etagOf("k", typ))
	wantStatus(t, created, http.StatusCreated)
	field := decode(t, created)
	if field["key"] != "rating" || created.Header().Get("ETag") != etagOf("kf", field) {
		t.Fatalf("created field = %v, ETag = %q", field, created.Header().Get("ETag"))
	}

	list := e.listOfType("Movies", nil, "movie")
	item := e.newItem(list["id"].(string), `{"title":"The Matrix","metadata":{"rating":5}}`)
	preview := e.call(http.MethodGet, "/api/v1/types/movie/fields/rating", "", "")
	wantStatus(t, preview, http.StatusOK)
	if decode(t, preview)["populated_item_count"] != float64(1) {
		t.Fatalf("preview = %s", preview.Body)
	}

	renamed := e.call(http.MethodPatch, "/api/v1/types/movie/fields/rating", `{"label":"Personal rating"}`, etagOf("kf", field))
	wantStatus(t, renamed, http.StatusOK)
	field = decode(t, renamed)
	if field["label"] != "Personal rating" || renamed.Header().Get("ETag") != etagOf("kf", field) {
		t.Fatalf("renamed field = %v, ETag = %q", field, renamed.Header().Get("ETag"))
	}

	typ = e.typ("movie")
	reordered := e.call(http.MethodPut, "/api/v1/types/movie/fields/order", `{"keys":["rating"]}`, etagOf("k", typ))
	wantStatus(t, reordered, http.StatusNoContent)
	if reordered.Header().Get("ETag") == "" {
		t.Fatal("reorder response omitted ETag")
	}
	field["version"] = field["version"].(float64) + 1

	deleted := e.call(http.MethodDelete, "/api/v1/types/movie/fields/rating", "", etagOf("kf", field))
	wantStatus(t, deleted, http.StatusOK)
	if decode(t, deleted)["populated_item_count"] != float64(1) {
		t.Fatalf("delete = %s", deleted.Body)
	}
	fetched := e.fetch("items", item["id"].(string))
	metadata, ok := fetched["metadata"].(map[string]any)
	if !ok || metadata["rating"] != nil {
		t.Errorf("metadata after deletion = %#v", fetched["metadata"])
	}

	locked := e.call(http.MethodDelete, "/api/v1/types/movie/fields/release_year", "", `"kf-1"`)
	p := wantProblem(t, locked, http.StatusUnprocessableEntity, "validation_failed")
	if len(p.Errors) != 1 || p.Errors[0].Field != "field" {
		t.Errorf("locked field problem = %+v", p)
	}
}

func TestF014B3_CustomTypeDefaultsCanBeUpdatedAfterFieldsExist(t *testing.T) {
	e := newTreeEnv(t)
	created := e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game","default_icon":"i:dices"}`, "")
	wantStatus(t, created, http.StatusCreated)
	typ := decode(t, created)
	if typ["default_icon"] != "i:dices" {
		t.Fatalf("created type default_icon = %#v", typ["default_icon"])
	}

	field := e.call(http.MethodPost, "/api/v1/types/board_game/fields", `{"label":"Rating","type":"number"}`, etagOf("k", typ))
	wantStatus(t, field, http.StatusCreated)
	typ = e.typ("board_game")
	updated := e.call(http.MethodPatch, "/api/v1/types/board_game", `{"allows_price":true,"default_sort_field":"rating","default_pinned_fields":["rating","price"]}`, etagOf("k", typ))
	wantStatus(t, updated, http.StatusOK)
	typ = decode(t, updated)
	if typ["name"] != "Board game" || typ["default_icon"] != "i:dices" || typ["allows_price"] != true || typ["default_sort_field"] != "rating" {
		t.Fatalf("updated type = %#v", typ)
	}
	pins, ok := typ["default_pinned_fields"].([]any)
	if !ok || len(pins) != 2 || pins[0] != "rating" || pins[1] != "price" {
		t.Fatalf("updated type pins = %#v", typ["default_pinned_fields"])
	}

	cleared := e.call(http.MethodPatch, "/api/v1/types/board_game", `{"default_icon":null,"default_pinned_fields":null}`, etagOf("k", typ))
	wantStatus(t, cleared, http.StatusOK)
	typ = decode(t, cleared)
	if typ["default_icon"] != nil || typ["default_pinned_fields"] != nil || typ["default_sort_field"] != "rating" {
		t.Fatalf("cleared defaults = %#v", typ)
	}

	invalid := e.call(http.MethodPatch, "/api/v1/types/board_game", `{"default_sort_field":"missing"}`, etagOf("k", typ))
	p := wantProblem(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	if len(p.Errors) != 1 || p.Errors[0].Field != "default_sort_field" {
		t.Fatalf("invalid defaults problem = %+v", p)
	}
	locked := e.call(http.MethodPatch, "/api/v1/types/movie", `{"name":"Films"}`, etagOf("k", e.typ("movie")))
	wantProblem(t, locked, http.StatusUnprocessableEntity, "validation_failed")
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
