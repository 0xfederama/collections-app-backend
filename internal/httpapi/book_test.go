package httpapi_test

import (
	"net/http"
	"reflect"
	"testing"
)

func TestF016B2_BuiltinBookTypeIsSeeded(t *testing.T) {
	e := newTreeEnv(t)
	typ := e.typ("book")
	if typ["name"] != "Book" || typ["is_locked"] != true || typ["allows_price"] != true || typ["default_sort_field"] != "first_publish_year" {
		t.Fatalf("book type = %#v", typ)
	}
	if !reflect.DeepEqual(typ["default_pinned_fields"], []any{"author", "pages", "stores"}) {
		t.Errorf("default pins = %v", typ["default_pinned_fields"])
	}
	var keys []string
	for _, f := range typ["fields"].([]any) {
		keys = append(keys, f.(map[string]any)["key"].(string))
	}
	want := []string{"author", "pages", "first_publish_year", "isbn", "stores", "openlibrary_id", "lookup_disabled"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("fields = %v, want %v", keys, want)
	}
}

func TestF016B2_BookItemValidation(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)

	it := e.newItem(lid, `{"title":"Dune","price":{"amount":"9.90","currency":"EUR"},"metadata":{"author":"Frank Herbert","pages":412,"first_publish_year":1965,"isbn":"978-0-441-17271-9","stores":["kobo"],"openlibrary_id":"OL893415W","lookup_disabled":true}}`)
	meta := it["metadata"].(map[string]any)
	if meta["author"] != "Frank Herbert" || meta["pages"] != float64(412) || meta["first_publish_year"] != float64(1965) || meta["lookup_disabled"] != true {
		t.Errorf("metadata = %v", meta)
	}
	if it["price"] == nil {
		t.Error("book price was not stored")
	}

	tests := []struct{ name, metadata string }{
		{"pages not a number", `{"pages":"many"}`},
		{"year not a number", `{"first_publish_year":"old"}`},
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
}

func TestF016B2_BookTypeIsLocked(t *testing.T) {
	e := newTreeEnv(t)
	wantProblem(t, e.call(http.MethodPatch, "/api/v1/types/book", `{"name":"Novels"}`, etagOf("k", e.typ("book"))), http.StatusUnprocessableEntity, "validation_failed")
	wantProblem(t, e.call(http.MethodDelete, "/api/v1/types/book/fields/isbn", "", `"kf-1"`), http.StatusUnprocessableEntity, "validation_failed")
}

func TestF016B2_NewBookItemsGetThePreferredStores(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO workspace_settings (workspace_id, preferred_stores) VALUES ($1, '{kobo,amazon}')`, e.ws)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	stores := func(it obj) any { return it["metadata"].(map[string]any)["stores"] }

	if got := stores(e.newItem(lid, `{"title":"Unset"}`)); !reflect.DeepEqual(got, []any{"kobo", "amazon"}) {
		t.Errorf("unset stores = %v, want the preferred stores", got)
	}
	if got := stores(e.newItem(lid, `{"title":"Null","metadata":{"stores":null}}`)); !reflect.DeepEqual(got, []any{"kobo", "amazon"}) {
		t.Errorf("null stores = %v, want the preferred stores", got)
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
