package httpapi_test

import (
	"testing"

	"github.com/google/uuid"
)

const priceBody = `{"title":"t","price":{"amount":"12.00","currency":"EUR"}}`

func TestF008B1_GenericItemsRejectPrice(t *testing.T) {
	e := newTreeEnv(t)
	gen := e.listOfKind("Gen", nil, "generic")["id"].(string)
	movie := e.listOfKind("Films", nil, "movie")["id"].(string)

	t.Run("create with a price is 422 on price", func(t *testing.T) {
		p := wantProblem(t, e.call("POST", "/api/v1/lists/"+gen+"/items", priceBody, ""), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "price" || p.Errors[0].Message != "generic items have no price" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		if rows, _ := e.page(gen, ""); len(rows) != 0 {
			t.Fatalf("rejected request created %d items", len(rows))
		}
	})

	t.Run("create with price null is fine", func(t *testing.T) {
		wantStatus(t, e.call("POST", "/api/v1/lists/"+gen+"/items", `{"title":"t","price":null}`, ""), 201)
	})

	t.Run("patch with a price is 422, price null is fine", func(t *testing.T) {
		it := e.titled(gen, "plain")
		p := wantProblem(t, e.patchItem(it, `{"price":{"min":"1.00","max":"2.00","currency":"EUR"}}`), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "price" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		if got := e.fetchItem(it["id"]); got["version"] != float64(1) || got["price"] != nil {
			t.Fatalf("rejected patch changed the item: %v", got)
		}
		wantStatus(t, e.patchItem(it, `{"price":null}`), 200)
	})

	t.Run("other kinds still accept a price", func(t *testing.T) {
		it := e.newItem(movie, priceBody)
		if it["price"] == nil {
			t.Fatalf("price lost: %v", it)
		}
		wantStatus(t, e.patchItem(it, `{"price":{"amount":"13.00","currency":"EUR"}}`), 200)
	})
}

func TestF008B1_GenericListsHaveNoPriceFields(t *testing.T) {
	e := newTreeEnv(t)

	t.Run("pinning price on a generic list is 422", func(t *testing.T) {
		p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"G","kind":"generic","pinned_fields":{"right":["price"]}}`, ""), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "pinned_fields.right[0]" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		g := e.listOfKind("G", nil, "generic")
		wantProblem(t, e.call("PATCH", "/api/v1/lists/"+g["id"].(string), `{"pinned_fields":{"under":["price"]}}`, etagOf("l", g)), 422, "validation_failed")
		// Empty slots stay fine.
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+g["id"].(string), `{"pinned_fields":{"under":[],"right":[]}}`, etagOf("l", g)), 200)
	})

	t.Run("sorting a generic list by price is 422", func(t *testing.T) {
		p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"G","kind":"generic","sort_mode":"price"}`, ""), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "sort_mode" {
			t.Fatalf("errors = %+v", p.Errors)
		}
		g := e.listOfKind("G2", nil, "generic")
		wantProblem(t, e.call("PATCH", "/api/v1/lists/"+g["id"].(string), `{"sort_mode":"price"}`, etagOf("l", g)), 422, "validation_failed")
		m := e.listOfKind("M", nil, "movie")
		wantProblem(t, e.call("PATCH", "/api/v1/lists/"+m["id"].(string), `{"kind":"generic","sort_mode":"price"}`, etagOf("l", m)), 422, "validation_failed")
		wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"M2","kind":"movie","sort_mode":"price"}`, ""), 201)
	})
}

func TestF008B1_KindChangeToGenericClearsPrice(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Films", nil, "movie")
	lid := l["id"].(string)
	priced := e.newItem(lid, priceBody)
	plain := e.titled(lid, "plain")
	trashed := e.newItem(lid, priceBody)
	other := e.newItem(e.listOfKind("Other", nil, "movie")["id"].(string), priceBody)
	e.exec(`UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, trashed["id"], e.trashBatch("item", uuid.MustParse(trashed["id"].(string))))

	l = decode(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"price","pinned_fields":{"under":["release_year"],"right":["price"]}}`, etagOf("l", l)))
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"generic"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["kind"] != "generic" || got["sort_mode"] != "manual" || got["pinned_fields"] != nil {
		t.Fatalf("list after kind change: %v", got)
	}
	if got["version"].(float64) <= l["version"].(float64) || rec.Header().Get("ETag") != etagOf("l", got) {
		t.Fatalf("version/ETag not bumped: %v %s", got, rec.Header().Get("ETag"))
	}
	if it := e.fetchItem(priced["id"]); it["price"] != nil || it["version"] != float64(2) {
		t.Fatalf("priced item after kind change: %v", it)
	}
	if it := e.fetchItem(plain["id"]); it["version"] != float64(1) {
		t.Fatalf("item without price was touched: %v", it)
	}
	var n int
	e.queryRow(`SELECT count(*) FROM items WHERE id = $1 AND price_min IS NULL AND price_max IS NULL AND currency IS NULL AND version = 2`, []any{trashed["id"]}, &n)
	if n != 1 {
		t.Fatal("trashed item kept its price")
	}
	if it := e.fetchItem(other["id"]); it["price"] == nil || it["version"] != float64(1) {
		t.Fatalf("item of another list changed: %v", it)
	}
}
