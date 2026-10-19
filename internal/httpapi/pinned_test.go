package httpapi_test

import (
	"reflect"
	"testing"
)

func pinnedOf(o obj) any { return o["pinned_fields"] }

func TestF009B2_PinnedFieldsLifecycle(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Films", nil, "movie")
	lid := l["id"].(string)
	if v, ok := l["pinned_fields"]; !ok || v != nil {
		t.Fatalf("new list: pinned_fields = %v (present %v), want null", v, ok)
	}

	// Set: an ordered list, stored, read back, ETag bumps.
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":["release_year","streaming"]}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	set := decode(t, rec)
	want := []any{"release_year", "streaming"}
	if !reflect.DeepEqual(pinnedOf(set), want) || set["version"] != float64(2) || rec.Header().Get("ETag") != `"l-2"` {
		t.Fatalf("after set: %v etag=%s", set, rec.Header().Get("ETag"))
	}
	if got := e.fetch("lists", lid); !reflect.DeepEqual(pinnedOf(got), want) {
		t.Fatalf("read back: %v", got)
	}
	for _, tl := range e.tree("lists") {
		if tl["id"] == lid && !reflect.DeepEqual(pinnedOf(tl), want) {
			t.Fatalf("tree: %v", tl)
		}
	}

	// The same value again, and an absent field, change nothing.
	wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":["release_year","streaming"]}`, `"l-2"`), 200)
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"name":"Films 2"}`, `"l-2"`)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"] != float64(3) || !reflect.DeepEqual(pinnedOf(got), want) {
		t.Fatalf("rename must keep pinned fields: %v", got)
	}
	if got := e.fetch("lists", lid); got["version"] != float64(3) {
		t.Fatalf("same-value PATCH bumped the version: %v", got)
	}

	// A stale version loses.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":null}`, `"l-1"`), 412, "version_conflict")

	// Move leaves it alone.
	f := e.folder("F", nil)
	rec = e.call("POST", "/api/v1/lists/"+lid+"/move", `{"folder_id":"`+f["id"].(string)+`"}`, `"l-3"`)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); !reflect.DeepEqual(pinnedOf(got), want) {
		t.Fatalf("move changed pinned fields: %v", got)
	}

	// An empty list means "pin nothing", not NULL.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":[]}`, `"l-4"`)
	wantStatus(t, rec, 200)
	empty := decode(t, rec)
	if !reflect.DeepEqual(pinnedOf(empty), []any{}) || empty["version"] != float64(5) {
		t.Fatalf("empty: %v", empty)
	}

	// null resets; resetting again is a no-op.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":null}`, etagOf("l", empty))
	wantStatus(t, rec, 200)
	reset := decode(t, rec)
	if v, ok := reset["pinned_fields"]; !ok || v != nil || reset["version"] != float64(6) {
		t.Fatalf("reset: %v", reset)
	}
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":null}`, etagOf("l", reset))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"] != float64(6) {
		t.Fatalf("second reset bumped: %v", got)
	}
}

func TestF009B2_PinnedFieldsOnCreate(t *testing.T) {
	e := newTreeEnv(t)
	rec := e.call("POST", "/api/v1/lists", `{"name":"Bikes","type":"motorcycle","pinned_fields":["model_year","price"]}`, "")
	wantStatus(t, rec, 201)
	if got := decode(t, rec); !reflect.DeepEqual(pinnedOf(got), []any{"model_year", "price"}) {
		t.Fatalf("created: %v", got)
	}
	rec = e.call("POST", "/api/v1/lists", `{"name":"Null","type":"movie","pinned_fields":null}`, "")
	wantStatus(t, rec, 201)
	if v, ok := decode(t, rec)["pinned_fields"]; !ok || v != nil {
		t.Fatalf("null on create: %v", v)
	}
	// Invalid on create changes nothing.
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"X","type":"generic","pinned_fields":["release_year"]}`, ""), 422, "validation_failed")
}

func TestF009B2_PinnedFieldsValidation(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Films", nil, "movie")
	lid := l["id"].(string)
	tests := []struct {
		name, body, field string
	}{
		{"unknown key", `{"pinned_fields":["make"]}`, "pinned_fields[0]"},
		{"TestF009B1 price on a movie", `{"pinned_fields":["release_year","price"]}`, "pinned_fields[1]"},
		{"duplicate", `{"pinned_fields":["release_year","release_year"]}`, "pinned_fields[1]"},
		{"three keys", `{"pinned_fields":["release_year","runtime_minutes","imdb_url"]}`, "pinned_fields"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, tt.body, etagOf("l", l)), 422, "validation_failed")
			if len(p.Errors) == 0 || p.Errors[0].Field != tt.field {
				t.Fatalf("errors = %+v, want first field %s", p.Errors, tt.field)
			}
		})
	}
	// The old object shape is no longer a valid pinned_fields (now an array).
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{"under":["release_year"]}}`, etagOf("l", l)), 400, "malformed_request")
	if got := e.fetch("lists", lid); got["version"] != l["version"] || got["pinned_fields"] != nil {
		t.Fatalf("rejected patches changed the list: %v", got)
	}
}

func TestF009B2_ChangeTypeResetsPinnedFields(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Films", nil, "movie")
	lid := l["id"].(string)
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":["release_year","streaming"]}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)

	// Same type keeps it.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["pinned_fields"] == nil || got["version"] != l["version"] {
		t.Fatalf("same type: %v", got)
	}

	// New type alone, and with another field, resets it.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"car","name":"Cars"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if v, ok := got["pinned_fields"]; !ok || v != nil || got["type"] != "car" || got["name"] != "Cars" {
		t.Fatalf("after type change: %v", got)
	}
	if cur := e.fetch("lists", lid); cur["pinned_fields"] != nil {
		t.Fatalf("stored value not reset: %v", cur)
	}

	// A type change that also sets pinned fields validates them against the new type.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"camera","pinned_fields":["make"]}`, etagOf("l", got)), 422, "validation_failed")
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"camera","pinned_fields":["megapixels"]}`, etagOf("l", got))
	wantStatus(t, rec, 200)
	if g := decode(t, rec); !reflect.DeepEqual(pinnedOf(g), []any{"megapixels"}) {
		t.Fatalf("type change plus pinned: %v", g)
	}
}

func TestF009B2_PinnedKeysPerType(t *testing.T) {
	e := newTreeEnv(t)
	// Every key a type exposes can be pinned on a list of that type; movie and tv_show have no "price" (F009B1).
	keys := map[string]string{"movie": `["release_year","streaming"]`,
		"tv_show":    `["first_air_year","seasons"]`,
		"motorcycle": `["price","power_hp"]`}
	for typ, k := range keys {
		rec := e.call("POST", "/api/v1/lists", `{"name":"`+typ+`","type":"`+typ+`","pinned_fields":`+k+`}`, "")
		wantStatus(t, rec, 201)
	}
}
