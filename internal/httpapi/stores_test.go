package httpapi_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/items"
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

	it := e.newItem(lid, `{"title":"Dune","metadata":{"isbn":"978-0-441-17271-9","openlibrary_id":"OL893415W","stores":["kobo","amazon","bookshop"]}}`)
	want := map[string]any{
		"kobo":     "https://www.kobo.com/search?query=9780441172719",
		"amazon":   "https://www.amazon.com/s?k=Dune",
		"bookshop": "https://bookshop.org/search?keywords=9780441172719",
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

func TestF016_StoreLinksFollowTheWorkspaceCountry(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO workspace_settings (workspace_id) SELECT id FROM workspaces ON CONFLICT DO NOTHING`)
	lid := e.listOfType("Books", nil, "book")["id"].(string)
	it := e.newItem(lid, `{"title":"Dune","metadata":{"isbn":"9780441172719","stores":["amazon","google_play_books","bookshop"]}}`)

	// New workspaces default to Italy.
	want := map[string]any{
		"amazon":            "https://www.amazon.it/s?k=Dune",
		"google_play_books": "https://play.google.com/store/search?q=9780441172719&c=books&gl=IT",
		"bookshop":          "https://bookshop.org/search?keywords=9780441172719",
	}
	if got, _ := storeLinks(it); !reflect.DeepEqual(got, want) {
		t.Fatalf("default country: store_links = %v, want %v", got, want)
	}

	// The links are computed on read, so a later change of the country moves them.
	e.exec(`UPDATE workspace_settings SET streaming_country_code = 'GB'`)
	want = map[string]any{
		"amazon":            "https://www.amazon.co.uk/s?k=Dune",
		"google_play_books": "https://play.google.com/store/search?q=9780441172719&c=books&gl=GB",
		"bookshop":          "https://uk.bookshop.org/search?keywords=9780441172719",
	}
	if got, _ := storeLinks(e.fetchItem(it["id"])); !reflect.DeepEqual(got, want) {
		t.Errorf("after GB: store_links = %v, want %v", got, want)
	}
	e.exec(`UPDATE workspace_settings SET streaming_country_code = 'ZZ'`)
	if got, _ := storeLinks(e.fetchItem(it["id"])); got["amazon"] != "https://www.amazon.com/s?k=Dune" {
		t.Errorf("unknown country: amazon = %v, want amazon.com", got["amazon"])
	}
}

// countingDB counts the statements sent to the database.
type countingDB struct {
	sqlc.DBTX
	n int
}

func (c *countingDB) Exec(ctx context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	c.n++
	return c.DBTX.Exec(ctx, q, a...)
}

func (c *countingDB) Query(ctx context.Context, q string, a ...any) (pgx.Rows, error) {
	c.n++
	return c.DBTX.Query(ctx, q, a...)
}

func (c *countingDB) QueryRow(ctx context.Context, q string, a ...any) pgx.Row {
	c.n++
	return c.DBTX.QueryRow(ctx, q, a...)
}

func TestF016_RowsCarryStoreLinks(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO workspace_settings (workspace_id) SELECT id FROM workspaces ON CONFLICT DO NOTHING`)
	books := e.listOfType("Books", nil, "book")["id"].(string)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	plain := e.listOfType("Plain", nil, "")["id"].(string)
	e.newItem(books, `{"title":"Dune","metadata":{"isbn":"9780441172719","stores":["kobo","amazon"]}}`)
	e.newItem(books, `{"title":"Dune Messiah","metadata":{"stores":[]}}`)
	e.newItem(books, `{"title":"Dune Third","metadata":{"stores":["bookshop"]}}`)
	e.newItem(films, `{"title":"Dune film","metadata":{}}`)
	e.newItem(plain, `{"title":"Dune plain","metadata":{}}`)

	check := func(where string, rows map[string]obj) {
		t.Helper()
		if got, _ := storeLinks(rows["Dune"]); !reflect.DeepEqual(got, map[string]any{
			"kobo":   "https://www.kobo.com/search?query=9780441172719",
			"amazon": "https://www.amazon.it/s?k=Dune",
		}) {
			t.Errorf("%s: Dune store_links = %v", where, got)
		}
		if got, ok := storeLinks(rows["Dune Messiah"]); !ok || len(got) != 0 {
			t.Errorf("%s: empty selection store_links = %v (present %v), want {}", where, got, ok)
		}
		if got, _ := storeLinks(rows["Dune Third"]); got["bookshop"] != "https://bookshop.org/search?keywords=Dune+Third" {
			t.Errorf("%s: title fallback store_links = %v", where, got)
		}
		for _, title := range []string{"Dune film", "Dune plain"} {
			if _, ok := storeLinks(rows[title]); ok {
				t.Errorf("%s: %q has store_links", where, title)
			}
		}
	}
	byTitle := func(rows []obj) map[string]obj {
		out := map[string]obj{}
		for _, r := range rows {
			out[r["title"].(string)] = r
		}
		return out
	}

	var listed []obj
	for _, id := range []string{books, films, plain} {
		rows, _ := e.page(id, "")
		listed = append(listed, rows...)
	}
	check("list", byTitle(listed))

	var found []obj
	for _, r := range e.search("q=Dune").Data {
		found = append(found, r.Item)
	}
	check("search", byTitle(found))
	found = nil
	for _, r := range e.recent("").Data {
		found = append(found, r.Item)
	}
	check("recent", byTitle(found))

	// A page costs a fixed number of statements, whatever its size: the stores
	// fields of all types and the settings, read once.
	db := &countingDB{DBTX: e.pool}
	var page []*items.Item
	for range 30 {
		typ := "book"
		page = append(page, &items.Item{Title: "x", Type: &typ, Metadata: []byte(`{"stores":["kobo"]}`)})
	}
	if err := items.FillStoreLinks(t.Context(), db, e.ws, page); err != nil {
		t.Fatal(err)
	}
	if db.n != 2 {
		t.Errorf("FillStoreLinks ran %d statements for 30 rows, want 2", db.n)
	}
	if page[29].StoreLinks["kobo"] == "" {
		t.Errorf("last row store_links = %v", page[29].StoreLinks)
	}
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
