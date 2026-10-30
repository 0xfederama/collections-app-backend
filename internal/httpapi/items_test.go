package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (e *treeEnv) newItem(listID string, body string) obj {
	e.t.Helper()
	rec := e.call("POST", "/api/v1/lists/"+listID+"/items", body, "")
	wantStatus(e.t, rec, 201)
	it := decode(e.t, rec)
	if rec.Header().Get("Location") != "/api/v1/items/"+it["id"].(string) || rec.Header().Get("ETag") != fmt.Sprintf(`"i-%v"`, it["version"]) {
		e.t.Fatalf("bad Location/ETag: %v / %v", rec.Header().Get("Location"), rec.Header().Get("ETag"))
	}
	return it
}

func (e *treeEnv) titled(listID, title string) obj {
	e.t.Helper()
	b, _ := json.Marshal(obj{"title": title})
	return e.newItem(listID, string(b))
}

func (e *treeEnv) patchItem(it obj, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("PATCH", "/api/v1/items/"+it["id"].(string), body, etagOf("i", it))
}

func (e *treeEnv) fetchItem(id any) obj {
	e.t.Helper()
	return e.fetch("items", id.(string))
}

// page fetches a page of items and returns its rows and page block.
func (e *treeEnv) page(listID, query string) ([]obj, obj) {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/lists/"+listID+"/items"+query, "", "")
	wantStatus(e.t, rec, 200)
	var body struct {
		Data []obj `json:"data"`
		Page obj   `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		e.t.Fatal(err)
	}
	return body.Data, body.Page
}

func titlesOf(rows []obj) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["title"].(string))
	}
	return out
}

func idsOf(rows ...obj) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["id"].(string))
	}
	return out
}

func idsJSON(ids ...string) string {
	b, _ := json.Marshal(obj{"ids": ids})
	return string(b)
}

// matrix is the body of a movie item: create it in a list of type movie. Movie
// items have no price (F009-B1); price behavior is tested on a wishlist list (F-018).
const matrix = `{"title":"The Matrix","website":"https://www.warnerbros.com/movies/matrix",
	"metadata":{"release_year":1999,"runtime_minutes":136,"imdb_url":"https://www.imdb.com/title/tt0133093/","streaming":{"country":"IT","platforms":["Netflix","Prime Video"]}}}`

func TestF014B2_ItemMetadataUsesWorkspaceFieldDefinitions(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position)
		SELECT $1, id, 'rating', 'Rating', 'number', 100 FROM type_definitions
		WHERE workspace_id = $1 AND key = 'movie'`, e.ws)
	list := e.listOfType("Films", nil, "movie")
	item := e.newItem(list["id"].(string), `{"title":"A film","metadata":{"rating":4.5}}`)
	if got := item["metadata"].(map[string]any)["rating"]; got != float64(4.5) {
		t.Fatalf("rating = %v, want 4.5", got)
	}
	wantProblem(t, e.call("POST", "/api/v1/lists/"+list["id"].(string)+"/items", `{"title":"A film","metadata":{"unknown":4.5}}`, ""), 400, "unknown_field")
}

func TestAT05_CreateItem(t *testing.T) {
	e := newTreeEnv(t)
	list := e.listOfType("Watchlist", nil, "movie")
	lid := list["id"].(string)

	t.Run("defaults", func(t *testing.T) {
		it := e.titled(lid, "  Plain  ")
		want := obj{"title": "Plain", "type": "movie", "image": nil, "price": nil, "website": nil,
			"content_version": float64(0), "sort_order": float64(0), "version": float64(1), "list_id": lid, "metadata": map[string]any{}}
		for k, v := range want {
			if !reflect.DeepEqual(it[k], v) {
				t.Errorf("%s = %#v, want %#v", k, it[k], v)
			}
		}
		if _, hasPath := it["path"]; hasPath {
			t.Error("create must not return a path")
		}
		for _, gone := range []string{"summary", "status", "official_website_url"} {
			if _, has := it[gone]; has {
				t.Errorf("item still carries %q", gone)
			}
		}
	})

	t.Run("movie round trip", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		if it["type"] != "movie" || it["sort_order"] != float64(1) {
			t.Fatalf("created: %v", it)
		}
		if it["price"] != nil {
			t.Fatalf("a movie item must have no price: %v", it["price"])
		}
		got := e.fetchItem(it["id"])
		meta := got["metadata"].(map[string]any)
		stream := meta["streaming"].(map[string]any)
		if meta["release_year"] != float64(1999) || stream["country"] != "IT" || !reflect.DeepEqual(stream["platforms"], []any{"Netflix", "Prime Video"}) {
			t.Fatalf("metadata = %v", meta)
		}
	})

	t.Run("TestF002B1_paid platforms round trip", func(t *testing.T) {
		it := e.newItem(lid, `{"title":"Paid","metadata":{"streaming":{"country":"IT","platforms":["Netflix","Prime Video"],"paid_platforms":["prime video"]}}}`)
		stream := e.fetchItem(it["id"])["metadata"].(map[string]any)["streaming"].(map[string]any)
		if !reflect.DeepEqual(stream["paid_platforms"], []any{"Prime Video"}) || !reflect.DeepEqual(stream["platforms"], []any{"Netflix", "Prime Video"}) {
			t.Fatalf("streaming = %v", stream)
		}
	})

	t.Run("TestF002B1_legacy item without paid platforms", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		stream := e.fetchItem(it["id"])["metadata"].(map[string]any)["streaming"].(map[string]any)
		if _, has := stream["paid_platforms"]; has {
			t.Fatalf("streaming = %v", stream)
		}
	})

	t.Run("appended last", func(t *testing.T) {
		a := e.titled(lid, "A")
		b := e.titled(lid, "B")
		if b["sort_order"].(float64) != a["sort_order"].(float64)+1 {
			t.Fatalf("sort_order a=%v b=%v", a["sort_order"], b["sort_order"])
		}
	})

	t.Run("list item_count follows", func(t *testing.T) {
		if c := e.fetch("lists", lid)["item_count"].(float64); c < 3 {
			t.Fatalf("item_count = %v", c)
		}
	})

	t.Run("missing and trashed list are 404", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/lists/"+uuid.NewString()+"/items", `{"title":"x"}`, ""), 404, "not_found")
		wantProblem(t, e.call("POST", "/api/v1/lists/"+e.trashList("T", nil)+"/items", `{"title":"x"}`, ""), 404, "not_found")
	})
}

func TestAT05_ItemFieldRules(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("L", nil, "movie")["id"].(string)
	tv := e.listOfType("TV", nil, "tv_show")["id"].(string)
	car := e.listOfType("Cars", nil, "vehicle")["id"].(string)
	bike := e.listOfType("Wishes", nil, "wishlist")["id"].(string)
	gen := e.list("Generic", nil)["id"].(string)
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name, list, body string
		status           int
		code             string
		field            string
	}{
		{"title empty", lid, `{"title":""}`, 422, "validation_failed", "title"},
		{"title blank", lid, `{"title":"   "}`, 422, "validation_failed", "title"},
		{"title 301", lid, `{"title":"` + long(301) + `"}`, 422, "validation_failed", "title"},
		{"url javascript", lid, `{"title":"t","website":"javascript:alert(1)"}`, 422, "validation_failed", "website"},
		{"url data", lid, `{"title":"t","website":"data:text/html,x"}`, 422, "validation_failed", "website"},
		{"url ftp", lid, `{"title":"t","website":"ftp://example.com/a"}`, 422, "validation_failed", "website"},
		{"url no scheme", lid, `{"title":"t","website":"example.com"}`, 422, "validation_failed", "website"},
		{"url no host", lid, `{"title":"t","website":"https://"}`, 422, "validation_failed", "website"},
		{"url empty", lid, `{"title":"t","website":""}`, 422, "validation_failed", "website"},
		{"url too long", lid, `{"title":"t","website":"https://example.com/` + long(2040) + `"}`, 422, "validation_failed", "website"},
		{"price min format", bike, `{"title":"t","price":{"min":"12.5","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price.min"},
		{"price comma", bike, `{"title":"t","price":{"min":"12,50","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price.min"},
		{"price negative", bike, `{"title":"t","price":{"min":"-1.00","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price.min"},
		{"price 11 digits", bike, `{"title":"t","price":{"min":"12345678901.00","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price.min"},
		{"price max format", bike, `{"title":"t","price":{"min":null,"max":"abc","currency":"EUR"}}`, 422, "validation_failed", "price.max"},
		{"price min above max", bike, `{"title":"t","price":{"min":"20.00","max":"10.00","currency":"EUR"}}`, 422, "validation_failed", "price.max"},
		{"price both null", bike, `{"title":"t","price":{"min":null,"max":null,"currency":"EUR"}}`, 422, "validation_failed", "price"},
		{"currency missing", bike, `{"title":"t","price":{"min":"1.00","max":null}}`, 422, "validation_failed", "price.currency"},
		{"currency lowercase", bike, `{"title":"t","price":{"min":"1.00","max":null,"currency":"eur"}}`, 422, "validation_failed", "price.currency"},
		{"currency 2 letters", bike, `{"title":"t","price":{"min":"1.00","max":null,"currency":"EU"}}`, 422, "validation_failed", "price.currency"},
		{"TestF007 amount format", bike, `{"title":"t","price":{"amount":"12.5","currency":"EUR"}}`, 422, "validation_failed", "price.amount"},
		{"TestF007 amount without currency", bike, `{"title":"t","price":{"amount":"12.50"}}`, 422, "validation_failed", "price.currency"},
		{"TestF007 amount with min", bike, `{"title":"t","price":{"amount":"12.50","min":"10.00","currency":"EUR"}}`, 422, "validation_failed", "price"},
		{"TestF007 amount with max", bike, `{"title":"t","price":{"amount":"12.50","max":"15.00","currency":"EUR"}}`, 422, "validation_failed", "price"},
		{"TestF009B1 price on a movie", lid, `{"title":"t","price":{"min":"1.00","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price"},
		{"TestF009B1 price on a tv_show", tv, `{"title":"t","price":{"min":"1.00","max":null,"currency":"EUR"}}`, 422, "validation_failed", "price"},
		{"metadata bad imdb", lid, `{"title":"t","metadata":{"imdb_url":"http://imdb.com/title/tt1234567/"}}`, 422, "validation_failed", "metadata.imdb_url"},
		{"generic with a key", gen, `{"title":"t","metadata":{"release_year":1999}}`, 400, "unknown_field", "metadata.release_year"},
		{"unknown metadata key", lid, `{"title":"t","metadata":{"unknown_movie_field":"x"}}`, 400, "unknown_field", "metadata.unknown_movie_field"},
		{"unknown nested streaming key", lid, `{"title":"t","metadata":{"streaming":{"country":"IT","x":1}}}`, 400, "unknown_field", "metadata.streaming.x"},
		{"unknown top-level key", lid, `{"title":"t","colour":"red"}`, 400, "unknown_field", "colour"},
		{"TestF007 type is no longer sent", lid, `{"title":"t","type":"movie"}`, 400, "unknown_field", "type"},
		{"TestF007 summary is gone", lid, `{"title":"t","summary":"x"}`, 400, "unknown_field", "summary"},
		{"TestF007 status is gone", lid, `{"title":"t","status":"x"}`, 400, "unknown_field", "status"},
		{"TestF007 official_website_url is renamed", lid, `{"title":"t","official_website_url":"https://example.com"}`, 400, "unknown_field", "official_website_url"},
		{"TestF007 metadata is checked against the list type", lid, `{"title":"t","metadata":{"make":"VW"}}`, 400, "unknown_field", "metadata.make"},
		{"TestF007 dropped car key", car, `{"title":"t","metadata":{"electric_range_km":400}}`, 400, "unknown_field", "metadata.electric_range_km"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call("POST", "/api/v1/lists/"+tt.list+"/items", tt.body, ""), tt.status, tt.code)
			if tt.status == 400 {
				if !strings.Contains(p.Detail, `"`+tt.field+`"`) {
					t.Errorf("detail = %q, want it to name %q", p.Detail, tt.field)
				}
				return
			}
			var fields []string
			for _, fe := range p.Errors {
				fields = append(fields, fe.Field)
			}
			if !slices.Contains(fields, tt.field) {
				t.Errorf("fields = %v, want %q", fields, tt.field)
			}
		})
	}
	if rows, _ := e.page(lid, ""); len(rows) != 0 {
		t.Fatalf("rejected requests created %d items", len(rows))
	}

	t.Run("accepted edge values", func(t *testing.T) {
		for _, body := range []string{
			`{"title":"` + long(300) + `"}`,
			`{"title":"t","website":"HTTP://Example.com/a?b=c#d"}`,
		} {
			wantStatus(t, e.call("POST", "/api/v1/lists/"+lid+"/items", body, ""), 201)
		}
		for _, body := range []string{
			`{"title":"t","price":{"min":"0.00","max":"9999999999.99","currency":"JPY"}}`,
			`{"title":"t","price":{"min":"5.00","max":"5.00","currency":"EUR"}}`,
			`{"title":"t","price":{"min":null,"max":"5.00","currency":"EUR"}}`,
			`{"title":"t","price":{"amount":"5.00","currency":"EUR"}}`,
			`{"title":"t","price":null,"website":null}`,
		} {
			wantStatus(t, e.call("POST", "/api/v1/lists/"+bike+"/items", body, ""), 201)
		}
	})
}

func TestF007_PriceAmountIsASinglePrice(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("L", nil, "wishlist")["id"].(string)

	it := e.newItem(lid, `{"title":"single","price":{"amount":"1200.00","currency":"EUR"}}`)
	p := it["price"].(map[string]any)
	if p["min"] != "1200.00" || p["max"] != "1200.00" || p["currency"] != "EUR" {
		t.Fatalf("price = %v, want min == max == 1200.00", p)
	}
	if _, has := p["amount"]; has {
		t.Error("responses carry min and max, not amount")
	}
	var minV, maxV string
	e.queryRow(`SELECT price_min::text, price_max::text FROM items WHERE id = $1`, []any{it["id"]}, &minV, &maxV)
	if minV != "1200.00" || maxV != "1200.00" {
		t.Fatalf("stored %s/%s", minV, maxV)
	}

	// PATCH: an amount replaces the whole price; the same single price again changes nothing.
	got := decode(t, e.patchItem(it, `{"price":{"amount":"999.90","currency":"USD"}}`))
	if p := got["price"].(map[string]any); p["min"] != "999.90" || p["max"] != "999.90" || p["currency"] != "USD" || got["version"] != float64(2) {
		t.Fatalf("patched: %v", got)
	}
	same := decode(t, e.patchItem(got, `{"price":{"amount":"999.90","currency":"USD"}}`))
	if same["version"] != float64(2) {
		t.Fatalf("an identical price bumped the version: %v", same["version"])
	}
	// The equal min/max form is the same price as the amount form.
	same = decode(t, e.patchItem(got, `{"price":{"min":"999.90","max":"999.90","currency":"USD"}}`))
	if same["version"] != float64(2) {
		t.Fatalf("min == max is the amount price, but the version bumped: %v", same["version"])
	}
	wantProblem(t, e.patchItem(got, `{"price":{"amount":"1.00","max":"2.00","currency":"USD"}}`), 422, "validation_failed")
	got = decode(t, e.patchItem(got, `{"price":null}`))
	if got["price"] != nil {
		t.Fatalf("price = %v, want cleared", got["price"])
	}
}

func TestPriceRoundTripIsExact(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("L", nil, "wishlist")["id"].(string)
	for _, price := range []string{"1299.00", "0.10", "0.01", "12.30", "9999999999.99", "100.00", "0.00"} {
		it := e.newItem(lid, fmt.Sprintf(`{"title":"p","price":{"min":%q,"max":%q,"currency":"EUR"}}`, price, price))
		got := e.fetchItem(it["id"])["price"].(map[string]any)
		if got["min"] != price || got["max"] != price {
			t.Errorf("price %s came back as %v", price, got)
		}
		var min, max string
		e.queryRow(`SELECT price_min::text, price_max::text FROM items WHERE id = $1`, []any{it["id"]}, &min, &max)
		if min != price || max != price {
			t.Errorf("price %s stored as %s/%s", price, min, max)
		}
	}
}

func (e *treeEnv) queryRow(sql string, args []any, dest ...any) {
	e.t.Helper()
	if err := e.pool.QueryRow(e.t.Context(), sql, args...).Scan(dest...); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
}

func TestAT05_UpdateItem(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("L", nil, "movie")["id"].(string)
	bike := e.listOfType("Wishes", nil, "wishlist")["id"].(string)

	t.Run("only supplied fields change and the version bumps", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		rec := e.patchItem(it, `{"title":"The Matrix Reloaded"}`)
		wantStatus(t, rec, 200)
		got := decode(t, rec)
		if got["title"] != "The Matrix Reloaded" || got["version"] != float64(2) || rec.Header().Get("ETag") != `"i-2"` {
			t.Fatalf("patched: %v etag=%s", got, rec.Header().Get("ETag"))
		}
		if got["website"] != "https://www.warnerbros.com/movies/matrix" || got["price"] != nil || got["metadata"].(map[string]any)["release_year"] != float64(1999) {
			t.Fatalf("untouched fields changed: %v", got)
		}
		if got["updated_at"] == it["updated_at"] {
			t.Error("updated_at not bumped")
		}
	})

	t.Run("null clears, absent keeps", func(t *testing.T) {
		it := e.newItem(bike, `{"title":"The Matrix","website":"https://www.warnerbros.com/movies/matrix","price":{"min":"1299.00","max":null,"currency":"EUR"}}`)
		rec := e.patchItem(it, `{"price":null,"website":null}`)
		wantStatus(t, rec, 200)
		got := decode(t, rec)
		if got["price"] != nil || got["website"] != nil || got["title"] != "The Matrix" {
			t.Fatalf("after null patch: %v", got)
		}
		var minV, maxV, cur *string
		e.queryRow(`SELECT price_min::text, price_max::text, currency FROM items WHERE id = $1`, []any{it["id"]}, &minV, &maxV, &cur)
		if minV != nil || maxV != nil || cur != nil {
			t.Fatalf("price columns not cleared: %v %v %v", minV, maxV, cur)
		}
	})

	t.Run("price set and replaced as a whole", func(t *testing.T) {
		it := e.titled(bike, "priced")
		it = decode(t, e.patchItem(it, `{"price":{"min":"3.00","max":"4.50","currency":"USD"}}`))
		it = decode(t, e.patchItem(it, `{"price":{"min":null,"max":"9.00","currency":"EUR"}}`))
		if p := it["price"].(map[string]any); p["min"] != nil || p["max"] != "9.00" || p["currency"] != "EUR" {
			t.Fatalf("price = %v", p)
		}
	})

	t.Run("TestF007 type is read-only: a type in the patch is an unknown field", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		wantProblem(t, e.patchItem(it, `{"type":"vehicle"}`), 400, "unknown_field")
		for _, gone := range []string{`{"summary":"x"}`, `{"status":"x"}`, `{"official_website_url":"https://example.com"}`} {
			wantProblem(t, e.patchItem(it, gone), 400, "unknown_field")
		}
		if after := e.fetchItem(it["id"]); after["type"] != "movie" || after["version"] != float64(1) {
			t.Fatalf("rejected patch changed the item: %v", after)
		}
	})

	t.Run("TestF007 metadata is validated against the list type", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		p := wantProblem(t, e.patchItem(it, `{"metadata":{"make":"VW"}}`), 400, "unknown_field")
		if !strings.Contains(p.Detail, "metadata.make") {
			t.Errorf("detail = %q", p.Detail)
		}
		got := decode(t, e.patchItem(it, `{"metadata":{"release_year":2001}}`))
		if got["type"] != "movie" || got["metadata"].(map[string]any)["release_year"] != float64(2001) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("metadata alone is validated against the current type and replaces it", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		got := decode(t, e.patchItem(it, `{"metadata":{"runtime_minutes":90}}`))
		if !reflect.DeepEqual(got["metadata"], map[string]any{"runtime_minutes": float64(90)}) {
			t.Fatalf("metadata = %v", got["metadata"])
		}
		wantProblem(t, e.patchItem(got, `{"metadata":{"make":"Ducati"}}`), 400, "unknown_field")
		got = decode(t, e.patchItem(got, `{"metadata":{"runtime_minutes":0}}`))
		if !reflect.DeepEqual(got["metadata"], map[string]any{"runtime_minutes": float64(0)}) {
			t.Fatalf("metadata = %v", got["metadata"])
		}
		got = decode(t, e.patchItem(got, `{"metadata":{}}`))
		if !reflect.DeepEqual(got["metadata"], map[string]any{}) {
			t.Fatalf("metadata = %v", got["metadata"])
		}
	})

	t.Run("field rules apply to patches", func(t *testing.T) {
		it := e.titled(lid, "rules")
		for _, body := range []string{`{"title":""}`, `{"website":"javascript:alert(1)"}`} {
			wantProblem(t, e.patchItem(it, body), 422, "validation_failed")
		}
		priced := e.titled(bike, "priced rules")
		for _, body := range []string{
			`{"price":{"min":"2.00","max":"1.00","currency":"EUR"}}`, `{"price":{"min":"1.00","max":null}}`,
		} {
			wantProblem(t, e.patchItem(priced, body), 422, "validation_failed")
		}
		wantProblem(t, e.patchItem(it, `{"nope":1}`), 400, "unknown_field")
		if after := e.fetchItem(it["id"]); after["version"] != float64(1) {
			t.Fatalf("rejected patches bumped the version: %v", after)
		}
	})

	t.Run("a patch that changes nothing does not bump", func(t *testing.T) {
		it := e.newItem(lid, matrix)
		for _, body := range []string{
			`{}`, `{"title":"The Matrix"}`,
			`{"metadata":{"release_year":1999,"runtime_minutes":136,"imdb_url":"https://www.imdb.com/title/tt0133093/","streaming":{"platforms":["Netflix","Prime Video"],"country":"IT"}}}`,
			`{"website":"https://www.warnerbros.com/movies/matrix"}`,
		} {
			rec := e.patchItem(it, body)
			wantStatus(t, rec, 200)
			if got := decode(t, rec); got["version"] != float64(1) || got["updated_at"] != it["updated_at"] || rec.Header().Get("ETag") != `"i-1"` {
				t.Fatalf("patch %s bumped: %v", body, got)
			}
		}
	})

	t.Run("stale If-Match is 412 with the current version, and wins over a bad body", func(t *testing.T) {
		it := e.titled(lid, "stale")
		wantStatus(t, e.patchItem(it, `{"title":"fresh"}`), 200)
		p := wantProblem(t, e.patchItem(it, `{"title":"late"}`), 412, "version_conflict")
		if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
			t.Fatalf("current_version = %v", p.CurrentVersion)
		}
		if got := e.fetchItem(it["id"]); got["title"] != "fresh" {
			t.Fatalf("stale write applied: %v", got)
		}
		wantProblem(t, e.patchItem(it, `{"metadata":{"bad":1}}`), 412, "version_conflict")
	})

	t.Run("missing or trashed item is 404", func(t *testing.T) {
		wantProblem(t, e.call("PATCH", "/api/v1/items/"+uuid.NewString(), `{"title":"x"}`, `"i-1"`), 404, "not_found")
		trashed := uuid.New()
		batch := e.trashBatch("item", trashed)
		e.exec(`INSERT INTO items (id, workspace_id, list_id, title, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, 'gone', 0, now(), $4)`, trashed, e.ws, lid, batch)
		wantProblem(t, e.call("PATCH", "/api/v1/items/"+trashed.String(), `{"title":"x"}`, `"i-1"`), 404, "not_found")
		wantProblem(t, e.call("GET", "/api/v1/items/"+trashed.String(), "", ""), 404, "not_found")
	})
}

func TestGetItemHasPathAndReadModel(t *testing.T) {
	e := newTreeEnv(t)
	media := e.folder("Media", nil)
	movies := e.folder("Movies", media["id"])
	list := e.list("Watchlist", movies["id"])
	lid := list["id"].(string)
	it := e.titled(lid, "Solo")

	rec := e.call("GET", "/api/v1/items/"+it["id"].(string), "", "")
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if rec.Header().Get("ETag") != `"i-1"` {
		t.Errorf("ETag = %s", rec.Header().Get("ETag"))
	}
	wantPath := []any{
		map[string]any{"type": "folder", "id": media["id"], "name": "Media"},
		map[string]any{"type": "folder", "id": movies["id"], "name": "Movies"},
		map[string]any{"type": "list", "id": lid, "name": "Watchlist"},
	}
	if !reflect.DeepEqual(got["path"], wantPath) {
		t.Fatalf("path = %v", got["path"])
	}

	// A list at the root: the path is just the list.
	root := e.list("Root", nil)
	rootItem := e.titled(root["id"].(string), "x")
	if p := e.fetchItem(rootItem["id"])["path"].([]any); len(p) != 1 || p[0].(map[string]any)["type"] != "list" {
		t.Fatalf("root path = %v", p)
	}

	t.Run("content_version is the highest saved version, 0 when none", func(t *testing.T) {
		if got["content_version"] != float64(0) {
			t.Fatalf("content_version = %v", got["content_version"])
		}
		for v := 1; v <= 3; v++ {
			e.exec(`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, $2, 'b', 1, '\x00')`, it["id"], v)
		}
		e.exec(`DELETE FROM item_content_versions WHERE item_id = $1 AND version = 1`, it["id"])
		if cv := e.fetchItem(it["id"])["content_version"]; cv != float64(3) {
			t.Fatalf("content_version = %v, want 3", cv)
		}
		rows, _ := e.page(lid, "")
		if rows[0]["content_version"] != float64(3) {
			t.Fatalf("summary content_version = %v", rows[0]["content_version"])
		}
	})

	t.Run("image: none, external url and upload", func(t *testing.T) {
		if got["image"] != nil {
			t.Fatalf("image = %v", got["image"])
		}
		e.exec(`UPDATE items SET image_url = 'https://img.example/a.png' WHERE id = $1`, it["id"])
		if img := e.fetchItem(it["id"])["image"]; !reflect.DeepEqual(img, map[string]any{"source": "url", "url": "https://img.example/a.png"}) {
			t.Fatalf("url image = %v", img)
		}
		rows, _ := e.page(lid, "")
		if !reflect.DeepEqual(rows[0]["image"], map[string]any{"url": "https://img.example/a.png"}) {
			t.Fatalf("url image summary = %v", rows[0]["image"])
		}

		att := uuid.New()
		e.exec(`INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, original_filename, media_type, size_bytes, width, height, sha256)
			VALUES ($1, $2, $3, $4, 'poster.png', 'image/png', 10, 800, 600, '\x00')`, att, e.ws, "images/"+att.String()+".png", "images/"+att.String()+".thumb.jpg")
		e.exec(`UPDATE items SET image_url = NULL, image_attachment_id = $2 WHERE id = $1`, it["id"], att)
		wantImg := map[string]any{
			"source": "upload", "attachment_id": att.String(), "url": "/api/v1/attachments/" + att.String() + "/file",
			"thumbnail_url": "/api/v1/attachments/" + att.String() + "/thumbnail", "width": float64(800), "height": float64(600), "original_filename": "poster.png",
		}
		if img := e.fetchItem(it["id"])["image"]; !reflect.DeepEqual(img, wantImg) {
			t.Fatalf("upload image = %v", img)
		}
		rows, _ = e.page(lid, "")
		wantSum := map[string]any{"url": "/api/v1/attachments/" + att.String() + "/file", "thumbnail_url": "/api/v1/attachments/" + att.String() + "/thumbnail"}
		if !reflect.DeepEqual(rows[0]["image"], wantSum) {
			t.Fatalf("upload image summary = %v", rows[0]["image"])
		}
		e.exec(`UPDATE attachments SET original_filename = NULL WHERE id = $1`, att)
		if img := e.fetchItem(it["id"])["image"].(map[string]any); img["original_filename"] != nil {
			t.Fatalf("original_filename = %v, want null", img["original_filename"])
		}
	})

	t.Run("a patch keeps the image", func(t *testing.T) {
		cur := e.fetchItem(it["id"])
		got := decode(t, e.patchItem(cur, `{"title":"Solo 2"}`))
		if !reflect.DeepEqual(got["image"], cur["image"]) {
			t.Fatalf("image changed: %v -> %v", cur["image"], got["image"])
		}
	})
}

func TestListItems(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("L", nil)["id"].(string)
	banana := e.titled(lid, "banana")
	apple := e.titled(lid, "Apple")
	cherry := e.titled(lid, "cherry")
	// Touch the first one so "updated" differs from the creation order.
	banana = decode(t, e.patchItem(banana, `{"website":"https://touched.example"}`))

	t.Run("sorts", func(t *testing.T) {
		tests := []struct{ query, want string }{
			{"", "banana,Apple,cherry"}, // the list's sort_mode is manual: creation order
			{"?sort=manual", "banana,Apple,cherry"},
			{"?sort=title", "Apple,banana,cherry"}, // case-insensitive
			{"?sort=updated", "banana,cherry,Apple"},
		}
		for _, tt := range tests {
			rows, _ := e.page(lid, tt.query)
			if got := strings.Join(titlesOf(rows), ","); got != tt.want {
				t.Errorf("%q: got %s, want %s", tt.query, got, tt.want)
			}
		}
	})

	t.Run("default sort is the list's sort_mode", func(t *testing.T) {
		list := e.fetch("lists", lid)
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"title"}`, etagOf("l", list)), 200)
		rows, _ := e.page(lid, "")
		if got := strings.Join(titlesOf(rows), ","); got != "Apple,banana,cherry" {
			t.Fatalf("got %s", got)
		}
		rows, _ = e.page(lid, "?sort=manual")
		if got := strings.Join(titlesOf(rows), ","); got != "banana,Apple,cherry" {
			t.Fatalf("explicit sort ignored: %s", got)
		}
	})

	t.Run("summary rows", func(t *testing.T) {
		rows, _ := e.page(lid, "?sort=manual")
		first := rows[0]
		if m, ok := first["metadata"].(map[string]any); !ok || len(m) != 0 {
			t.Errorf("ItemSummary metadata = %v, want {}", first["metadata"])
		}
		if first["id"] != banana["id"] || first["version"] != float64(2) || first["website"] != "https://touched.example" || first["type"] != nil || first["image"] != nil || first["price"] != nil {
			t.Fatalf("row = %v", first)
		}
		_ = apple
		_ = cherry
	})

	t.Run("pagination", func(t *testing.T) {
		rows, page := e.page(lid, "?sort=manual&limit=2")
		if len(rows) != 2 || page["total"] != float64(3) || page["limit"] != float64(2) || page["offset"] != float64(0) {
			t.Fatalf("page 1: %d rows %v", len(rows), page)
		}
		rows, page = e.page(lid, "?sort=manual&limit=2&offset=2")
		if len(rows) != 1 || rows[0]["title"] != "cherry" || page["total"] != float64(3) || page["offset"] != float64(2) {
			t.Fatalf("page 2: %v %v", titlesOf(rows), page)
		}
		rows, page = e.page(lid, "?limit=2&offset=10")
		if len(rows) != 0 || page["total"] != float64(3) {
			t.Fatalf("past the end: %v %v", rows, page)
		}
		_, page = e.page(lid, "")
		if page["limit"] != float64(50) || page["offset"] != float64(0) {
			t.Fatalf("defaults: %v", page)
		}
		_, page = e.page(lid, "?limit=200")
		if page["limit"] != float64(200) {
			t.Fatalf("limit 200: %v", page)
		}
	})

	t.Run("pages are stable and do not overlap", func(t *testing.T) {
		big := e.list("Big", nil)["id"].(string)
		for i := range 7 {
			e.titled(big, "same") // equal titles: the id breaks the tie
			_ = i
		}
		seen := map[string]bool{}
		for off := 0; off < 7; off += 3 {
			rows, _ := e.page(big, fmt.Sprintf("?sort=title&limit=3&offset=%d", off))
			for _, r := range rows {
				if seen[r["id"].(string)] {
					t.Fatalf("item %v on two pages", r["id"])
				}
				seen[r["id"].(string)] = true
			}
		}
		if len(seen) != 7 {
			t.Fatalf("saw %d distinct items, want 7", len(seen))
		}
	})

	t.Run("trashed items are not listed, trashed or missing list is 404", func(t *testing.T) {
		other := e.list("Other", nil)["id"].(string)
		e.item(other, true)
		if rows, page := e.page(other, ""); len(rows) != 0 || page["total"] != float64(0) {
			t.Fatalf("rows=%v page=%v", rows, page)
		}
		wantProblem(t, e.call("GET", "/api/v1/lists/"+e.trashList("T", nil)+"/items", "", ""), 404, "not_found")
		wantProblem(t, e.call("GET", "/api/v1/lists/"+uuid.NewString()+"/items", "", ""), 404, "not_found")
	})

	t.Run("items of other lists are not listed", func(t *testing.T) {
		other := e.list("Elsewhere", nil)["id"].(string)
		e.titled(other, "zzz")
		rows, _ := e.page(lid, "")
		if slices.Contains(titlesOf(rows), "zzz") {
			t.Fatal("foreign item listed")
		}
	})
}

func TestListItemsFilter(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("L", nil)["id"].(string)
	for _, title := range []string{"The Matrix", "matrix reloaded", "100% Pure", "snake_case", "snakeXcase", "back\\slash", "Ünïcode Ñ", "Other"} {
		e.titled(lid, title)
	}
	tests := []struct {
		q    string
		want []string
	}{
		{"matrix", []string{"The Matrix", "matrix reloaded"}},
		{"MAT", []string{"The Matrix", "matrix reloaded"}},
		{"%", []string{"100% Pure"}},
		{"_", []string{"snake_case"}},
		{"e_c", []string{"snake_case"}},
		{"\\", []string{"back\\slash"}},
		{"k\\s", []string{"back\\slash"}},
		{"ünï", []string{"Ünïcode Ñ"}},
		{"nothing", nil},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			rows, page := e.page(lid, "?sort=title&q="+urlQueryEscape(tt.q))
			got := titlesOf(rows)
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Fatalf("q=%q got %v, want %v", tt.q, got, want)
			}
			if int(page["total"].(float64)) != len(tt.want) {
				t.Fatalf("total = %v, want %d", page["total"], len(tt.want))
			}
		})
	}
	t.Run("q with pagination counts all matches", func(t *testing.T) {
		rows, page := e.page(lid, "?q=matrix&limit=1")
		if len(rows) != 1 || page["total"] != float64(2) {
			t.Fatalf("rows=%d page=%v", len(rows), page)
		}
	})
	t.Run("blank q parameter is rejected", func(t *testing.T) {
		wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?q=", "", ""), 400, "invalid_parameter")
	})
}

func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func TestF013B4_ListItemsDirectorSortAndFilter(t *testing.T) {
	e := newTreeEnv(t)
	movies := e.listOfType("Films", nil, "movie")
	lid := movies["id"].(string)
	mk := func(title string, director any) obj {
		body := obj{"title": title, "lookup": false}
		if director != nil {
			body["metadata"] = obj{"director": director}
		}
		b, _ := json.Marshal(body)
		return e.newItem(lid, string(b))
	}
	mk("Zulu", "Jane Campion")
	mk("Alpha", "Jane Campion")
	mk("Middle", "Bong Joon-ho")
	mk("No director", nil)

	rows, page := e.page(lid, "?sort=director")
	got := titlesOf(rows)
	if len(got) != 4 || got[0] != "Middle" || got[3] != "No director" || page["total"] != float64(4) {
		t.Fatalf("director sort = %v total=%v, want Bong Joon-ho first and missing directors last", got, page["total"])
	}
	if middle := got[1:3]; (middle[0] != "Alpha" || middle[1] != "Zulu") && (middle[0] != "Zulu" || middle[1] != "Alpha") {
		t.Fatalf("director sort ties = %v, want Alpha and Zulu", middle)
	}

	rows, page = e.page(lid, "?sort=director&q=campion")
	got = titlesOf(rows)
	if len(got) != 2 || ((got[0] != "Alpha" || got[1] != "Zulu") && (got[0] != "Zulu" || got[1] != "Alpha")) || page["total"] != float64(2) {
		t.Fatalf("director filter = %v total=%v, want Alpha and Zulu total=2", got, page["total"])
	}

	tv := e.listOfType("Shows", nil, "tv_show")
	e.newItem(tv["id"].(string), `{"title":"Show","lookup":false}`)
	wantProblem(t, e.call("GET", "/api/v1/lists/"+tv["id"].(string)+"/items?sort=director", "", ""), 400, "invalid_parameter")
}

func TestMoveItem(t *testing.T) {
	e := newTreeEnv(t)
	src := e.list("Src", nil)["id"].(string)
	dst := e.list("Dst", nil)["id"].(string)
	a := e.titled(src, "a")
	b := e.titled(src, "b")
	e.titled(dst, "x")
	e.titled(dst, "y")

	move := func(it obj, list string) *httptest.ResponseRecorder {
		return e.call("POST", "/api/v1/items/"+it["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, list), etagOf("i", it))
	}

	rec := move(a, dst)
	wantStatus(t, rec, 200)
	moved := decode(t, rec)
	if moved["list_id"] != dst || moved["sort_order"] != float64(2) || moved["version"] != float64(2) || rec.Header().Get("ETag") != `"i-2"` {
		t.Fatalf("moved: %v etag=%s", moved, rec.Header().Get("ETag"))
	}
	if rows, _ := e.page(dst, ""); strings.Join(titlesOf(rows), ",") != "x,y,a" {
		t.Fatalf("dst = %v", titlesOf(rows))
	}
	if rows, page := e.page(src, ""); strings.Join(titlesOf(rows), ",") != "b" || page["total"] != float64(1) {
		t.Fatalf("src = %v", titlesOf(rows))
	}
	if e.fetch("lists", dst)["item_count"] != float64(3) || e.fetch("lists", src)["item_count"] != float64(1) {
		t.Error("item_count did not follow the move")
	}

	t.Run("stale If-Match is 412", func(t *testing.T) {
		p := wantProblem(t, move(a, src), 412, "version_conflict") // a is at version 2 now
		if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
			t.Fatalf("current_version = %v", p.CurrentVersion)
		}
		if e.fetchItem(a["id"])["list_id"] != dst {
			t.Fatal("stale move applied")
		}
	})
	t.Run("destination missing or trashed is 409", func(t *testing.T) {
		wantProblem(t, move(b, uuid.NewString()), 409, "destination_unavailable")
		wantProblem(t, move(b, e.trashList("T", nil)), 409, "destination_unavailable")
		if got := e.fetchItem(b["id"]); got["list_id"] != src || got["version"] != float64(1) {
			t.Fatalf("failed move changed the item: %v", got)
		}
	})
	t.Run("missing or trashed item is 404", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/items/"+uuid.NewString()+"/move", fmt.Sprintf(`{"list_id":%q}`, dst), `"i-1"`), 404, "not_found")
	})
	t.Run("404 comes before 409, 412 before 409", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/items/"+uuid.NewString()+"/move", fmt.Sprintf(`{"list_id":%q}`, uuid.NewString()), `"i-1"`), 404, "not_found")
		stale := obj{"id": b["id"], "version": float64(9)}
		wantProblem(t, move(stale, uuid.NewString()), 412, "version_conflict")
	})
	t.Run("moving within the same list appends last", func(t *testing.T) {
		cur := e.fetchItem(b["id"])
		rec := move(cur, src)
		wantStatus(t, rec, 200)
		if got := decode(t, rec); got["sort_order"] != float64(2) || got["version"] != float64(2) {
			t.Fatalf("got %v", got) // the old slot is not reused: max+1 among the active siblings, itself included
		}
	})
	t.Run("missing list_id is rejected", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/items/"+a["id"].(string)+"/move", `{}`, `"i-2"`), 422, "validation_failed")
	})
}

func TestAT21_ReorderItems(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("L", nil)["id"].(string)
	a, b, c := e.titled(lid, "a"), e.titled(lid, "b"), e.titled(lid, "c")
	order := func() string {
		rows, _ := e.page(lid, "?sort=manual")
		return strings.Join(titlesOf(rows), ",")
	}
	reorder := func(ids ...string) *httptest.ResponseRecorder {
		return e.call("PUT", "/api/v1/lists/"+lid+"/items/order", idsJSON(ids...), "")
	}

	t.Run("a valid order rewrites sort_order to 0..n-1 without bumping versions", func(t *testing.T) {
		wantStatus(t, reorder(idsOf(c, a, b)...), 204)
		if got := order(); got != "c,a,b" {
			t.Fatalf("order = %s", got)
		}
		rows, _ := e.page(lid, "?sort=manual")
		for i, r := range rows {
			if r["sort_order"] != float64(i) || r["version"] != float64(1) {
				t.Fatalf("row %d = %v", i, r)
			}
		}
	})

	t.Run("a set mismatch is 409 order_stale and changes nothing", func(t *testing.T) {
		trashed := uuid.New()
		e.exec(`INSERT INTO items (id, workspace_id, list_id, title, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, 'gone', 9, now(), $4)`,
			trashed, e.ws, lid, e.trashBatch("item", trashed))
		foreign := e.titled(e.list("Other", nil)["id"].(string), "foreign")
		cases := map[string][]string{
			"duplicate": {a["id"].(string), a["id"].(string), b["id"].(string)},
			"missing":   idsOf(c, a),
			"extra":     append(idsOf(c, a, b), uuid.NewString()),
			"foreign":   {c["id"].(string), a["id"].(string), foreign["id"].(string)},
			"trashed":   {c["id"].(string), a["id"].(string), trashed.String()},
			"empty":     {},
		}
		for name, ids := range cases {
			t.Run(name, func(t *testing.T) {
				wantProblem(t, reorder(ids...), 409, "order_stale")
				if got := order(); got != "c,a,b" {
					t.Fatalf("order changed to %s", got)
				}
			})
		}
	})

	t.Run("works when sort_mode is not manual", func(t *testing.T) {
		list := e.fetch("lists", lid)
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"title"}`, etagOf("l", list)), 200)
		wantStatus(t, reorder(idsOf(b, c, a)...), 204)
		rows, _ := e.page(lid, "?sort=manual")
		if got := strings.Join(titlesOf(rows), ","); got != "b,c,a" {
			t.Fatalf("manual order = %s", got)
		}
		rows, _ = e.page(lid, "")
		if got := strings.Join(titlesOf(rows), ","); got != "a,b,c" {
			t.Fatalf("title order = %s", got)
		}
	})

	t.Run("an empty list reorders to empty", func(t *testing.T) {
		empty := e.list("Empty", nil)["id"].(string)
		wantStatus(t, e.call("PUT", "/api/v1/lists/"+empty+"/items/order", `{"ids":[]}`, ""), 204)
	})

	t.Run("missing or trashed list is 404", func(t *testing.T) {
		wantProblem(t, e.call("PUT", "/api/v1/lists/"+uuid.NewString()+"/items/order", `{"ids":[]}`, ""), 404, "not_found")
		wantProblem(t, e.call("PUT", "/api/v1/lists/"+e.trashList("T", nil)+"/items/order", `{"ids":[]}`, ""), 404, "not_found")
	})
}

func TestItemsConcurrentCreatesGetDistinctOrder(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("L", nil)["id"].(string)
	const n = 8
	done := make(chan struct{}, n)
	for i := range n {
		go func() {
			defer func() { done <- struct{}{} }()
			rec := e.call("POST", "/api/v1/lists/"+lid+"/items", fmt.Sprintf(`{"title":"i%d"}`, i), "")
			if rec.Code != 201 {
				t.Errorf("status = %d: %s", rec.Code, rec.Body)
			}
		}()
	}
	for range n {
		<-done
	}
	rows, page := e.page(lid, "?sort=manual")
	if page["total"] != float64(n) {
		t.Fatalf("total = %v", page["total"])
	}
	seen := map[float64]bool{}
	for _, r := range rows {
		seen[r["sort_order"].(float64)] = true
	}
	if len(seen) != n {
		t.Fatalf("sort_order values not distinct: %v", seen)
	}
}

func TestF007_MoveItemAcrossTypes(t *testing.T) {
	e := newTreeEnv(t)
	films := e.listOfType("Films", nil, "movie")["id"].(string)
	moreFilms := e.listOfType("More films", nil, "movie")["id"].(string)
	shows := e.listOfType("Shows", nil, "tv_show")["id"].(string)
	plain := e.list("Plain", nil)["id"].(string)
	it := e.newItem(films, matrix)
	move := func(it obj, list string) *httptest.ResponseRecorder {
		return e.call("POST", "/api/v1/items/"+it["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, list), etagOf("i", it))
	}

	for _, dest := range []string{shows, plain} {
		p := wantProblem(t, move(it, dest), 409, "type_mismatch")
		if p.Type != "https://collection-app.invalid/problems/type-mismatch" {
			t.Errorf("type = %q", p.Type)
		}
		got := e.fetchItem(it["id"])
		if got["list_id"] != films || got["version"] != float64(1) || got["metadata"].(map[string]any)["release_year"] != float64(1999) {
			t.Fatalf("a rejected move changed the item: %v", got)
		}
	}

	rec := move(it, moreFilms)
	wantStatus(t, rec, 200)
	if moved := decode(t, rec); moved["list_id"] != moreFilms || moved["type"] != "movie" || moved["metadata"].(map[string]any)["release_year"] != float64(1999) {
		t.Fatalf("moved: %v", moved)
	}

	// A destination that is missing is still destination_unavailable, not a type mismatch.
	wantProblem(t, move(e.fetchItem(it["id"]), uuid.NewString()), 409, "destination_unavailable")
}

func TestF014B4_ListItemsSortsByDefinitionAndFilters(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Films", nil, "movie")
	lid := l["id"].(string)
	mk := func(title string, year any) obj {
		body := obj{"title": title}
		if year != nil {
			body["metadata"] = obj{"release_year": year}
		}
		b, _ := json.Marshal(body)
		return e.newItem(lid, string(b))
	}
	late := mk("late", 2005)
	none := mk("none", nil)
	early := mk("early", 1990)
	tie := mk("tie", 1990)
	mid := mk("mid", 2000)

	// Ascending, empty last, ties by id.
	first, second := early, tie
	if second["id"].(string) < first["id"].(string) {
		first, second = second, first
	}
	want := idsOf(first, second, mid, late, none)

	t.Run("explicit sort", func(t *testing.T) {
		rows, _ := e.page(lid, "?sort=release_year")
		if got := idsOf(rows...); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", titlesOf(rows), want)
		}
	})

	t.Run("paging and q keep working", func(t *testing.T) {
		rows, page := e.page(lid, "?sort=release_year&limit=2&offset=2")
		if got := idsOf(rows...); !reflect.DeepEqual(got, want[2:4]) || page["total"].(float64) != 5 {
			t.Fatalf("page: got %v total %v", titlesOf(rows), page["total"])
		}
		rows, page = e.page(lid, "?sort=release_year&q=LATE")
		if got := titlesOf(rows); !reflect.DeepEqual(got, []string{"late"}) || page["total"].(float64) != 1 {
			t.Fatalf("q: got %v total %v", got, page["total"])
		}
	})

	t.Run("definition default and manual override preserve item order", func(t *testing.T) {
		manual := []string{"late", "none", "early", "tie", "mid"}
		rows, _ := e.page(lid, "")
		if got := idsOf(rows...); !reflect.DeepEqual(got, want) {
			t.Fatalf("default field sort: got %v", titlesOf(rows))
		}
		cur := e.fetch("lists", lid)
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"manual"}`, etagOf("l", cur)), 200)
		rows, _ = e.page(lid, "")
		if got := titlesOf(rows); !reflect.DeepEqual(got, manual) {
			t.Fatalf("manual: got %v", got)
		}
		cur = e.fetch("lists", lid)
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"release_year"}`, etagOf("l", cur)), 200)
		rows, _ = e.page(lid, "")
		if got := idsOf(rows...); !reflect.DeepEqual(got, want) {
			t.Fatalf("field sort after switching back: %v", titlesOf(rows))
		}
		var orders []int
		for _, id := range idsOf(late, none, early, tie, mid) {
			var so int
			e.queryRow("SELECT sort_order FROM items WHERE id = $1", []any{id}, &so)
			orders = append(orders, so)
		}
		if !reflect.DeepEqual(orders, []int{0, 1, 2, 3, 4}) {
			t.Fatalf("sort_order changed: %v", orders)
		}
	})

	t.Run("no-type list answers 400 invalid_parameter", func(t *testing.T) {
		g := e.list("Plain", nil)
		e.titled(g["id"].(string), "x")
		wantProblem(t, e.call("GET", "/api/v1/lists/"+g["id"].(string)+"/items?sort=type_key", "", ""), 400, "invalid_parameter")
	})

	t.Run("unknown definition field answers 400 invalid_parameter", func(t *testing.T) {
		wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?sort=random", "", ""), 400, "invalid_parameter")
	})
}

func TestF014B4_CustomFieldSortAndCombinedFilter(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position)
		SELECT $1, id, 'rating', 'Rating', 'number', 100 FROM type_definitions
		WHERE workspace_id = $1 AND key = 'movie'`, e.ws)
	l := e.listOfType("Ratings", nil, "movie")
	lid := l["id"].(string)
	cur := e.fetch("lists", lid)
	gotList := decode(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"pinned_fields":["rating"]}`, etagOf("l", cur)))
	if !reflect.DeepEqual(gotList["pinned_fields"], []any{"rating"}) {
		t.Fatalf("custom field pin = %v", gotList["pinned_fields"])
	}
	for _, body := range []string{
		`{"title":"Match ten","metadata":{"rating":10}}`,
		`{"title":"Match two","metadata":{"rating":2}}`,
		`{"title":"Match five","metadata":{"rating":5}}`,
		`{"title":"Match missing"}`,
	} {
		e.newItem(lid, body)
	}
	rows, _ := e.page(lid, "?sort=rating")
	if got := strings.Join(titlesOf(rows), ","); got != "Match two,Match five,Match ten,Match missing" {
		t.Fatalf("numeric sort = %s", got)
	}
	rows, page := e.page(lid, "?sort=rating&q=match&filter_field=rating&filter_min=4&filter_max=10")
	if got := strings.Join(titlesOf(rows), ","); got != "Match five,Match ten" || page["total"] != float64(2) {
		t.Fatalf("combined title and field filter = %s, page %v", got, page)
	}
	bad := wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?filter_field=rating&filter_min=10&filter_max=2", "", ""), 400, "invalid_parameter")
	if bad.Status != 400 {
		t.Fatalf("invalid range = %+v", bad)
	}
}

func TestF002B4_ListItemsSortByPrice(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Wishlist", nil, "wishlist")
	lid := l["id"].(string)
	mk := func(title string, min, max any) obj {
		body := obj{"title": title}
		if min != nil {
			body["price"] = obj{"min": min, "max": max, "currency": "EUR"}
		}
		b, _ := json.Marshal(body)
		return e.newItem(lid, string(b))
	}
	nopriceB := mk("b-none", nil, nil)
	nopriceA := mk("A-none", nil, nil)
	exp := mk("expensive", "1299.00", nil)
	rng := mk("range", "20.50", "500.00") // min 20.50 sorts before a single 25.00
	cheap := mk("Cheap", "9.99", nil)
	tieB := mk("tie-b", "25.00", nil)
	tieA := mk("Tie-a", "25.00", "25.00")
	want := []string{"Cheap", "range", "Tie-a", "tie-b", "expensive", "A-none", "b-none"}
	_ = []obj{nopriceB, nopriceA, exp, rng, cheap, tieB, tieA}

	t.Run("order: min ascending, no price last, ties by title", func(t *testing.T) {
		rows, _ := e.page(lid, "?sort=price")
		if got := titlesOf(rows); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("paging is stable", func(t *testing.T) {
		var got []string
		for off := 0; off < len(want); off += 3 {
			rows, page := e.page(lid, fmt.Sprintf("?sort=price&limit=3&offset=%d", off))
			if page["total"].(float64) != float64(len(want)) {
				t.Fatalf("total = %v", page["total"])
			}
			got = append(got, titlesOf(rows)...)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("paged: got %v, want %v", got, want)
		}
	})

	t.Run("stored sort_mode price is the default, override wins", func(t *testing.T) {
		cur := e.fetch("lists", lid)
		rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"price"}`, etagOf("l", cur))
		wantStatus(t, rec, 200)
		if decode(t, rec)["sort_mode"] != "price" {
			t.Fatalf("sort_mode not stored: %s", rec.Body)
		}
		rows, _ := e.page(lid, "")
		if got := titlesOf(rows); !reflect.DeepEqual(got, want) {
			t.Fatalf("default price: got %v", got)
		}
		rows, _ = e.page(lid, "?sort=title")
		if got := titlesOf(rows); got[0] != "A-none" {
			t.Fatalf("override title: got %v", got)
		}
	})

	t.Run("?sort=price works on a list stored as manual, and create accepts it", func(t *testing.T) {
		other := e.listOfType("Other", nil, "wishlist")
		oid := other["id"].(string)
		e.titled(oid, "x")
		rows, _ := e.page(oid, "?sort=price")
		if len(rows) != 1 {
			t.Fatalf("got %d rows", len(rows))
		}
		rec := e.call("POST", "/api/v1/lists", `{"name":"P","type":"wishlist","sort_mode":"price"}`, "")
		wantStatus(t, rec, 201)
		if decode(t, rec)["sort_mode"] != "price" {
			t.Fatalf("created sort_mode: %s", rec.Body)
		}
	})

	t.Run("invalid sort is still 400", func(t *testing.T) {
		wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?sort=cost", "", ""), 400, "invalid_parameter")
	})
}
