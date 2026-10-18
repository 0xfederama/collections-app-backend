package httpapi_test

import (
	"testing"

	"github.com/google/uuid"
)

const priceBody = `{"title":"t","price":{"amount":"12.00","currency":"EUR"}}`

// f009NoPriceKinds are the kinds whose items have no price (F009-B1, subsuming
// F008-B1 for generic); f009PriceKinds those that do.
var (
	f009NoPriceKinds = []string{"generic", "movie", "tv_show"}
	f009PriceKinds   = []string{"motorcycle", "car", "camera"}
)

func TestF009B1_PriceOnlyOnPriceKinds(t *testing.T) {
	e := newTreeEnv(t)
	lists := map[string]string{}
	for _, kind := range append(append([]string{}, f009PriceKinds...), f009NoPriceKinds...) {
		lists[kind] = e.listOfKind("L-"+kind, nil, kind)["id"].(string)
	}

	t.Run("create with a price is 422 on the kinds that have none", func(t *testing.T) {
		for _, kind := range f009NoPriceKinds {
			p := wantProblem(t, e.call("POST", "/api/v1/lists/"+lists[kind]+"/items", priceBody, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "price" || p.Errors[0].Message != "items of this kind have no price" {
				t.Errorf("%s: errors = %+v", kind, p.Errors)
			}
			if rows, _ := e.page(lists[kind], ""); len(rows) != 0 {
				t.Errorf("%s: rejected request created %d items", kind, len(rows))
			}
		}
	})

	t.Run("create with a price is fine on the kinds that have one", func(t *testing.T) {
		for _, kind := range f009PriceKinds {
			it := e.newItem(lists[kind], priceBody)
			if p := it["price"].(map[string]any); p["min"] != "12.00" || p["max"] != "12.00" || p["currency"] != "EUR" {
				t.Errorf("%s: price = %v", kind, p)
			}
		}
	})

	t.Run("create with price null is fine everywhere", func(t *testing.T) {
		for _, kind := range f009NoPriceKinds {
			wantStatus(t, e.call("POST", "/api/v1/lists/"+lists[kind]+"/items", `{"title":"t","price":null}`, ""), 201)
		}
	})

	t.Run("patch: a price is rejected without one, accepted with one", func(t *testing.T) {
		for _, kind := range f009NoPriceKinds {
			it := e.titled(lists[kind], "plain")
			p := wantProblem(t, e.patchItem(it, `{"price":{"min":"1.00","max":"2.00","currency":"EUR"}}`), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "price" {
				t.Errorf("%s: errors = %+v", kind, p.Errors)
			}
			if got := e.fetchItem(it["id"]); got["version"] != float64(1) || got["price"] != nil {
				t.Errorf("%s: rejected patch changed the item: %v", kind, got)
			}
			wantStatus(t, e.patchItem(it, `{"price":null}`), 200)
		}
		bike := e.newItem(lists["motorcycle"], priceBody)
		wantStatus(t, e.patchItem(bike, `{"price":{"amount":"13.00","currency":"EUR"}}`), 200)
	})
}

func TestF009B1_PriceKindsPinsAndSort(t *testing.T) {
	e := newTreeEnv(t)

	t.Run("price is not a pinnable key without a price", func(t *testing.T) {
		for _, kind := range f009NoPriceKinds {
			p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"P","kind":"`+kind+`","pinned_fields":["price"]}`, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "pinned_fields[0]" {
				t.Errorf("%s: errors = %+v", kind, p.Errors)
			}
		}
		wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"B","kind":"motorcycle","pinned_fields":["price"]}`, ""), 201)
		// An empty pin list stays fine on a kind that has no price.
		g := e.listOfKind("G", nil, "generic")
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+g["id"].(string), `{"pinned_fields":[]}`, etagOf("l", g)), 200)
	})

	t.Run("sort_mode price is rejected without a price", func(t *testing.T) {
		for _, kind := range f009NoPriceKinds {
			p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"S","kind":"`+kind+`","sort_mode":"price"}`, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "sort_mode" {
				t.Errorf("%s: errors = %+v", kind, p.Errors)
			}
		}
		wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"M","kind":"motorcycle","sort_mode":"price"}`, ""), 201)
	})

	t.Run("?sort=price is 400 invalid_parameter without a price", func(t *testing.T) {
		movie := e.listOfKind("Films", nil, "movie")["id"].(string)
		e.titled(movie, "x")
		wantProblem(t, e.call("GET", "/api/v1/lists/"+movie+"/items?sort=price", "", ""), 400, "invalid_parameter")
		bike := e.listOfKind("Bikes", nil, "motorcycle")["id"].(string)
		e.titled(bike, "x")
		wantStatus(t, e.call("GET", "/api/v1/lists/"+bike+"/items?sort=price", "", ""), 200)
	})
}

func TestF009B1_KindChangeToKindWithoutPriceClearsPrice(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfKind("Bikes", nil, "motorcycle")
	lid := l["id"].(string)
	priced := e.newItem(lid, priceBody)
	plain := e.titled(lid, "plain")
	trashed := e.newItem(lid, priceBody)
	other := e.newItem(e.listOfKind("Other", nil, "motorcycle")["id"].(string), priceBody)
	e.exec(`UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, trashed["id"], e.trashBatch("item", uuid.MustParse(trashed["id"].(string))))

	l = decode(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"price","pinned_fields":["model_year","price"]}`, etagOf("l", l)))
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"kind":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["kind"] != "movie" || got["sort_mode"] != "release_year" || got["pinned_fields"] != nil {
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
