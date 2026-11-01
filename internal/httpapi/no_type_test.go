package httpapi_test

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// API 0.11.0: the built-in `generic` type is gone. A list with no type has
// `type: null`, no fields, no price and only the built-in sort modes.

func TestNoType_CreateListDefaultsToNull(t *testing.T) {
	e := newTreeEnv(t)
	for name, body := range map[string]string{
		"omitted": `{"name":"A"}`,
		"null":    `{"name":"B","type":null}`,
	} {
		rec := e.call("POST", "/api/v1/lists", body, "")
		wantStatus(t, rec, 201)
		l := decode(t, rec)
		if v, ok := l["type"]; !ok || v != nil || l["sort_mode"] != "manual" {
			t.Errorf("%s: list = %v", name, l)
		}
		if got := e.fetch("lists", l["id"].(string)); got["type"] != nil {
			t.Errorf("%s: GET type = %v", name, got["type"])
		}
	}
	found := 0
	for _, l := range e.tree("lists") {
		if l["type"] == nil {
			found++
		}
	}
	if found != 2 {
		t.Errorf("GET /tree lists with type null = %d, want 2", found)
	}
}

func TestNoType_ListRules(t *testing.T) {
	e := newTreeEnv(t)
	for _, body := range map[string]string{
		"price sort":    `{"name":"n","sort_mode":"price"}`,
		"field sort":    `{"name":"n","sort_mode":"release_year"}`,
		"unknown field": `{"name":"n","sort_mode":"type_key"}`,
	} {
		wantProblem(t, e.call("POST", "/api/v1/lists", body, ""), 422, "validation_failed")
	}
	wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"n","sort_mode":"title"}`, ""), 201)
	l := e.list("Plain", nil)
	wantStatus(t, e.call("PATCH", "/api/v1/lists/"+l["id"].(string), `{"sort_mode":"updated"}`, etagOf("l", l)), 200)
}

func TestNoType_ItemsHaveNoMetadataOrPrice(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("Plain", nil)["id"].(string)
	it := e.titled(lid, "Thing")
	if v, ok := it["type"]; !ok || v != nil || !reflect.DeepEqual(it["metadata"], map[string]any{}) {
		t.Fatalf("item = %v", it)
	}
	wantStatus(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"Empty","metadata":{}}`, ""), 201)
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"t","metadata":{"release_year":1999}}`, ""), 400, "unknown_field")
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", priceBody, ""), 422, "validation_failed")
	wantProblem(t, e.patchItem(it, `{"metadata":{"make":"VW"}}`), 400, "unknown_field")
	wantProblem(t, e.patchItem(it, `{"price":{"amount":"1.00","currency":"EUR"}}`), 422, "validation_failed")
	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?sort=price", "", ""), 400, "invalid_parameter")
	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?sort=release_year", "", ""), 400, "invalid_parameter")
	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?filter=director&filter_value=x", "", ""), 400, "invalid_parameter")

	rows, _ := e.page(lid, "")
	if len(rows) != 2 || rows[0]["type"] != nil || !reflect.DeepEqual(rows[0]["metadata"], map[string]any{}) {
		t.Errorf("rows = %v", rows)
	}
	if got := e.fetchItem(it["id"]); got["type"] != nil {
		t.Errorf("GET item type = %v", got["type"])
	}

	// Search and recent items report a null type_name.
	res := e.search("q=Thing")
	if len(res.Data) != 1 || res.Data[0].Item["type"] != nil || res.Data[0].TypeName != nil {
		t.Errorf("search = %+v", res.Data)
	}
}

func TestNoType_MoveItems(t *testing.T) {
	e := newTreeEnv(t)
	a := e.list("A", nil)["id"].(string)
	b := e.list("B", nil)["id"].(string)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	it := e.titled(a, "Thing")
	move := func(it obj, list string) int {
		return e.call("POST", "/api/v1/items/"+it["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, list), etagOf("i", it)).Code
	}
	if code := move(it, b); code != http.StatusOK {
		t.Fatalf("move between no-type lists = %d", code)
	}
	it = e.fetchItem(it["id"])
	wantProblem(t, e.call("POST", "/api/v1/items/"+it["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, films), etagOf("i", it)), 409, "type_mismatch")
	m := e.newItem(films, matrix)
	wantProblem(t, e.call("POST", "/api/v1/items/"+m["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, a), etagOf("i", m)), 409, "type_mismatch")
}

func TestNoType_ChangeTypeToAndFromNullClearsItems(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Bikes", nil, "vehicle")
	lid := l["id"].(string)
	it := e.newItem(lid, `{"title":"Monster","metadata":{"displacement_cc":937}}`)

	// To no type: metadata and price go, the sort falls back to manual.
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"type":null,"sort_mode":"manual"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)
	if v, ok := l["type"]; !ok || v != nil {
		t.Fatalf("list after type null = %v", l)
	}
	got := e.fetchItem(it["id"])
	if got["type"] != nil || got["price"] != nil || !reflect.DeepEqual(got["metadata"], map[string]any{}) || got["version"] == it["version"] {
		t.Fatalf("item after type null = %v", got)
	}

	// Same type (null) is a no-op: no bump.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":null}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	if decode(t, rec)["version"] != l["version"] {
		t.Error("sending null for a list with no type bumped the version")
	}

	// From no type to a type: metadata of the item is validated against it from now on.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)
	if l["type"] != "movie" || l["sort_mode"] != "release_year" {
		t.Fatalf("list after type movie = %v", l)
	}
	got = e.fetchItem(it["id"])
	if got["type"] != "movie" || !reflect.DeepEqual(got["metadata"], map[string]any{}) {
		t.Fatalf("item after type movie = %v", got)
	}
	wantStatus(t, e.patchItem(got, `{"metadata":{"release_year":1999}}`), 200)
}

func TestNoType_GenericIsNotAType(t *testing.T) {
	e := newTreeEnv(t)
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"n","type":"generic"}`, ""), 422, "validation_failed")
}
