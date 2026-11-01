package httpapi_test

import (
	"reflect"
	"testing"
)

func TestF016B2_BuiltinBookTypeIsSeeded(t *testing.T) {
	e := newTreeEnv(t)
	typ := e.typ("book")
	if typ["name"] != "Books" || typ["allows_price"] != false || typ["default_sort_field"] != nil {
		t.Fatalf("book type = %#v", typ)
	}
	var keys []string
	for _, f := range typ["fields"].([]any) {
		keys = append(keys, f.(map[string]any)["key"].(string))
	}
	want := []string{"author", "pages", "isbn", "stores", "openlibrary_id", "lookup_disabled"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("fields = %v, want %v", keys, want)
	}
}

func TestF016B2_BookItemValidation(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)

	it := e.newItem(lid, `{"title":"Dune","metadata":{"author":"Frank Herbert","pages":412,"isbn":"978-0-441-17271-9","stores":["kobo"],"openlibrary_id":"OL893415W","lookup_disabled":true}}`)
	meta := it["metadata"].(map[string]any)
	if meta["author"] != "Frank Herbert" || meta["pages"] != float64(412) || meta["lookup_disabled"] != true {
		t.Errorf("metadata = %v", meta)
	}
	if it["price"] != nil {
		t.Error("book has no price since F-018")
	}

	tests := []struct{ name, metadata string }{
		{"pages not a number", `{"pages":"many"}`},
		{"author not text", `{"author":3}`},
		{"unknown store", `{"stores":["audible"]}`},
		{"lookup_disabled not boolean", `{"lookup_disabled":"yes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":`+tt.metadata+`}`, ""), 422, "validation_failed")
		})
	}
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":{"director":"x"}}`, ""), 400, "unknown_field")
	// The first publication year was removed from the book type (0.13.0).
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":{"first_publish_year":1965}}`, ""), 400, "unknown_field")
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":{"stores":["openlibrary"]}}`, ""), 422, "validation_failed")
}

func TestF016B2_NewBookItemsGetThePreferredStores(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO workspace_settings (workspace_id, preferred_store) VALUES ($1, 'kobo')`, e.ws)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	stores := func(it obj) any { return it["metadata"].(map[string]any)["stores"] }

	if got := stores(e.newItem(lid, `{"title":"Unset"}`)); !reflect.DeepEqual(got, []any{"kobo"}) {
		t.Errorf("unset stores = %v, want [preferred_store]", got)
	}
	if got := stores(e.newItem(lid, `{"title":"Null","metadata":{"stores":null}}`)); !reflect.DeepEqual(got, []any{"kobo"}) {
		t.Errorf("null stores = %v, want [preferred_store]", got)
	}
	if got := stores(e.newItem(lid, `{"title":"Empty","metadata":{"stores":[]}}`)); !reflect.DeepEqual(got, []any{}) {
		t.Errorf("explicit [] = %v, want []", got)
	}
	if got := stores(e.newItem(lid, `{"title":"Own","metadata":{"stores":["bookshop"]}}`)); !reflect.DeepEqual(got, []any{"bookshop"}) {
		t.Errorf("explicit stores = %v, want [bookshop]", got)
	}

	// A type without a stores field is untouched.
	movies := e.listOfType("Movies", nil, "movie")["id"].(string)
	if meta := e.newItem(movies, `{"title":"Heat"}`)["metadata"].(map[string]any); meta["stores"] != nil {
		t.Errorf("movie metadata = %v", meta)
	}
}
