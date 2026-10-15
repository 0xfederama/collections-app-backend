package httpapi_test

import (
	"reflect"
	"testing"
)

func pinnedOf(o obj) any { return o["pinned_fields"] }

func TestF002B3_PinnedFieldsLifecycle(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Films", nil, "movie")
	lid := l["id"].(string)
	if v, ok := l["pinned_fields"]; !ok || v != nil {
		t.Fatalf("new list: pinned_fields = %v (present %v), want null", v, ok)
	}

	// Set: stored, read back, ETag bumps.
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{"under":["release_year","streaming"],"right":["price"]}}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	set := decode(t, rec)
	want := map[string]any{"under": []any{"release_year", "streaming"}, "right": []any{"price"}}
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
	wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{"under":["release_year","streaming"],"right":["price"]}}`, `"l-2"`), 200)
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

	// Empty slots mean "pin nothing", not NULL; both slots are returned.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{}}`, `"l-4"`)
	wantStatus(t, rec, 200)
	empty := decode(t, rec)
	if !reflect.DeepEqual(pinnedOf(empty), map[string]any{"under": []any{}, "right": []any{}}) || empty["version"] != float64(5) {
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

func TestF002B3_PinnedFieldsOnCreate(t *testing.T) {
	e := newTreeEnv(t)
	rec := e.call("POST", "/api/v1/lists", `{"name":"Bikes","kind":"motorcycle","pinned_fields":{"under":["model_year"],"right":["price","power_hp"]}}`, "")
	wantStatus(t, rec, 201)
	if got := decode(t, rec); !reflect.DeepEqual(pinnedOf(got), map[string]any{"under": []any{"model_year"}, "right": []any{"price", "power_hp"}}) {
		t.Fatalf("created: %v", got)
	}
	rec = e.call("POST", "/api/v1/lists", `{"name":"Null","kind":"movie","pinned_fields":null}`, "")
	wantStatus(t, rec, 201)
	if v, ok := decode(t, rec)["pinned_fields"]; !ok || v != nil {
		t.Fatalf("null on create: %v", v)
	}
	// Invalid on create changes nothing.
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"X","kind":"generic","pinned_fields":{"under":["release_year"]}}`, ""), 422, "validation_failed")
}

func TestF002B3_PinnedFieldsValidation(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Films", nil, "movie")
	lid := l["id"].(string)
	tests := []struct {
		name, body, field string
	}{
		{"unknown key", `{"pinned_fields":{"under":["make"]}}`, "pinned_fields.under[0]"},
		{"unknown key right", `{"pinned_fields":{"under":["price"],"right":["release_year","bogus"]}}`, "pinned_fields.right[1]"},
		{"in both slots", `{"pinned_fields":{"under":["price"],"right":["price"]}}`, "pinned_fields.right[0]"},
		{"duplicate", `{"pinned_fields":{"under":["price","price"]}}`, "pinned_fields.under[1]"},
		{"seven keys", `{"pinned_fields":{"under":["a","b","c","d","e","f","g"]}}`, "pinned_fields.under"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, tt.body, etagOf("l", l)), 422, "validation_failed")
			if len(p.Errors) == 0 || p.Errors[0].Field != tt.field {
				t.Fatalf("errors = %+v, want first field %s", p.Errors, tt.field)
			}
		})
	}
	// Unknown object keys are an unknown-field 400.
	p := wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{"above":["price"]}}`, etagOf("l", l)), 400, "unknown_field")
	_ = p
	if got := e.fetch("lists", lid); got["version"] != l["version"] || got["pinned_fields"] != nil {
		t.Fatalf("rejected patches changed the list: %v", got)
	}
}

func TestF002B3_ChangeKindResetsPinnedFields(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Films", nil, "movie")
	lid := l["id"].(string)
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":{"under":["release_year"],"right":["price"]}}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)

	// Same kind keeps it.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["pinned_fields"] == nil || got["version"] != l["version"] {
		t.Fatalf("same kind: %v", got)
	}

	// New kind alone, and with another field, resets it.
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"car","name":"Cars"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if v, ok := got["pinned_fields"]; !ok || v != nil || got["kind"] != "car" || got["name"] != "Cars" {
		t.Fatalf("after kind change: %v", got)
	}
	if cur := e.fetch("lists", lid); cur["pinned_fields"] != nil {
		t.Fatalf("stored value not reset: %v", cur)
	}

	// A kind change that also sets pinned fields validates them against the new kind.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"camera","pinned_fields":{"under":["make"]}}`, etagOf("l", got)), 422, "validation_failed")
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"camera","pinned_fields":{"under":["megapixels"]}}`, etagOf("l", got))
	wantStatus(t, rec, 200)
	if g := decode(t, rec); !reflect.DeepEqual(pinnedOf(g), map[string]any{"under": []any{"megapixels"}, "right": []any{}}) {
		t.Fatalf("kind change plus pinned: %v", g)
	}
}

func TestF002B3_PinnedKeysPerKind(t *testing.T) {
	e := newTreeEnv(t)
	// Every key a kind exposes can be pinned on a list of that kind; "price" is common to all.
	keys := map[string]string{"generic": `["price"]`, "movie": `["release_year","runtime_minutes","imdb_url","streaming","price"]`,
		"tv_show": `["first_air_year","seasons","episodes_per_season","episode_minutes","streaming","price"]`}
	for kind, k := range keys {
		rec := e.call("POST", "/api/v1/lists", `{"name":"`+kind+`","kind":"`+kind+`","pinned_fields":{"right":`+k+`}}`, "")
		wantStatus(t, rec, 201)
	}
}
