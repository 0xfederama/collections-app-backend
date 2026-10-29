package httpapi_test

import (
	"strings"
	"testing"
)

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

func TestStreamingLink_RowsCarryItAndWholeObjectRoundTrips(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	it := e.newItem(lid, `{"title":"Matrix","metadata":{"streaming":{"country":"IT","platforms":["Netflix"]}}}`)
	link := "https://www.justwatch.com/it/movie/the-matrix"
	// The link is only ever written by the lookup: simulate it.
	e.exec(`UPDATE items SET metadata = jsonb_set(metadata, '{streaming,link}', to_jsonb($2::text)), version = version + 1 WHERE id = $1`, it["id"], link)
	it = e.fetchItem(it["id"])

	rows, _ := e.page(lid, "")
	if s, _ := rows[0]["metadata"].(map[string]any)["streaming"].(map[string]any); s["link"] != link {
		t.Fatalf("row streaming = %v, want the link", s)
	}

	// A client that round-trips the whole streaming object (even with another link) is not rejected.
	rec := e.patchItem(it, `{"metadata":{"streaming":{"country":"IT","platforms":["Netflix","Max"],"link":"https://evil.example/"}}}`)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if s, _ := got["metadata"].(map[string]any)["streaming"].(map[string]any); s["link"] != link || len(s["platforms"].([]any)) != 2 {
		t.Errorf("streaming after round trip = %v", s)
	}

	// Filtering and sorting on streaming look at the platforms, not at the link.
	if rows, _ := e.page(lid, "?filter_field=streaming&filter_value=justwatch"); len(rows) != 0 {
		t.Errorf("filter matched the link: %v", titlesOf(rows))
	}
	if rows, _ := e.page(lid, "?filter_field=streaming&filter_value=max"); len(rows) != 1 {
		t.Errorf("filter on a platform matched %d rows, want 1", len(rows))
	}
}

// TestB4a_NoLinkFields: store_links and streaming.platform_links were removed from
// the API (F-019 B4a, spec 0.17.0). Every item response (create, get, list rows,
// search and recent items) omits them, even when the item has the inputs that
// used to produce them: a stores selection and a streaming platform.
func TestB4a_NoLinkFields(t *testing.T) {
	e := newRecentEnv(t)
	books := e.listOfType("Books", nil, "book")["id"].(string)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	book := e.newItem(books, `{"title":"Dune","metadata":{"isbn":"9780441172719","stores":["kobo","amazon"]}}`)
	movie := e.newItem(films, `{"title":"Dune film","metadata":{"streaming":{"country":"IT","platforms":["Netflix"]}}}`)

	var searched, recent []obj
	for _, r := range e.search(q("Dune")).Data {
		searched = append(searched, r.Item)
	}
	for _, r := range e.recent("").Data {
		recent = append(recent, r.Item)
	}
	listed := func(listIDs ...string) []obj {
		var out []obj
		for _, id := range listIDs {
			rows, _ := e.page(id, "")
			out = append(out, rows...)
		}
		return out
	}

	tests := []struct {
		name  string
		items []obj
	}{
		{"book item create", []obj{book}},
		{"movie item create", []obj{movie}},
		{"book item get", []obj{e.fetchItem(book["id"])}},
		{"movie item get", []obj{e.fetchItem(movie["id"])}},
		{"list rows", listed(books, films)},
		{"search", searched},
		{"recent items", recent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.items) == 0 {
				t.Fatal("no items returned")
			}
			for _, it := range tt.items {
				meta, _ := it["metadata"].(map[string]any)
				if _, ok := meta["store_links"]; ok {
					t.Errorf("%v: store_links present: %v", it["title"], meta)
				}
				streaming, _ := meta["streaming"].(map[string]any)
				if _, ok := streaming["platform_links"]; ok {
					t.Errorf("%v: streaming.platform_links present: %v", it["title"], streaming)
				}
				// The inputs must still be there, so the absence above is not vacuous.
				switch it["title"] {
				case "Dune":
					if meta["stores"] == nil {
						t.Errorf("Dune lost its stores selection: %v", meta)
					}
				case "Dune film":
					if streaming["platforms"] == nil {
						t.Errorf("Dune film lost its streaming platforms: %v", meta)
					}
				}
			}
		})
	}
}
