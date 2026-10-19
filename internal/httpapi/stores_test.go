package httpapi_test

import (
	"reflect"
	"strings"
	"testing"
)

// storeLinks returns the store_links of an item response and whether it is present.
func storeLinks(it obj) (map[string]any, bool) {
	meta, _ := it["metadata"].(map[string]any)
	links, ok := meta["store_links"].(map[string]any)
	return links, ok
}

func TestF016B4_ItemStoreLinksAreComputedOnRead(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)

	it := e.newItem(lid, `{"title":"Dune","metadata":{"isbn":"978-0-441-17271-9","openlibrary_id":"OL893415W","stores":["kobo","openlibrary","bookshop"]}}`)
	want := map[string]any{
		"kobo":        "https://www.kobo.com/search?query=9780441172719",
		"openlibrary": "https://openlibrary.org/works/OL893415W",
		"bookshop":    "https://bookshop.org/search?keywords=9780441172719",
	}
	if got, ok := storeLinks(it); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("create: store_links = %v (present %v), want %v", got, ok, want)
	}
	if got, ok := storeLinks(e.fetchItem(it["id"])); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("GET: store_links = %v (present %v), want %v", got, ok, want)
	}

	var stored bool
	e.queryRow(`SELECT metadata ? 'store_links' FROM items WHERE id = $1`, []any{it["id"]}, &stored)
	if stored {
		t.Error("store_links was written to the stored metadata")
	}

	// A selection without a usable link is an empty object, not an absent key.
	empty := e.newItem(lid, `{"title":"Empty","metadata":{"stores":[]}}`)
	if got, ok := storeLinks(empty); !ok || len(got) != 0 {
		t.Errorf("empty selection: store_links = %v (present %v), want {}", got, ok)
	}
}

func TestF016B4_NonStoresTypeHasNoStoreLinks(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	it := e.newItem(lid, `{"title":"Matrix","metadata":{"release_year":1999}}`)
	if _, ok := storeLinks(it); ok {
		t.Errorf("movie item has store_links: %v", it["metadata"])
	}
}

func TestF016B4_StoreLinksCannotBeWritten(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":{"store_links":{}}}`, ""), 400, "unknown_field")

	it := e.newItem(lid, `{"title":"Dune","metadata":{"stores":["kobo"]}}`)
	wantProblem(t, e.patchItem(it, `{"metadata":{"store_links":{"kobo":"https://evil.example/"}}}`), 400, "unknown_field")
}

func TestF016B4_StoresFieldValidation(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	tests := []struct{ name, metadata string }{
		{"unknown key", `{"stores":["kobo","audible"]}`},
		{"duplicate key", `{"stores":["kobo","kobo"]}`},
		{"not an array", `{"stores":"kobo"}`},
		{"non-string entry", `{"stores":[1]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"x","metadata":`+tt.metadata+`}`, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || !strings.HasPrefix(p.Errors[0].Field, "metadata.stores") {
				t.Errorf("errors = %+v, want one error on metadata.stores", p.Errors)
			}
		})
	}
	t.Run("a stores field is not a custom field type", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/types/book/fields", `{"label":"Shops","type":"stores"}`, `"k-1"`), 422, "validation_failed")
	})
}

func TestF016B4_StoresFieldIsNotSortableOrFilterable(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	e.newItem(lid, `{"title":"Dune","metadata":{"stores":["kobo"]}}`)

	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?sort=stores", "", ""), 400, "invalid_parameter")
	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?filter_field=stores&filter_value=kobo", "", ""), 400, "invalid_parameter")
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"By store","type":"book","sort_mode":"stores"}`, ""), 422, "validation_failed")
	wantProblem(t, e.call("PATCH", "/api/v1/types/book", `{"default_sort_field":"stores"}`, `"k-1"`), 422, "validation_failed")
}
