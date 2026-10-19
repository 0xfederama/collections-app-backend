package httpapi_test

import (
	"testing"

	"github.com/google/uuid"
)

const priceBody = `{"title":"t","price":{"amount":"12.00","currency":"EUR"}}`

// f009NoPriceTypes are the types whose items have no price (F009-B1, subsuming
// F008-B1 for lists with no type, ""); f009PriceTypes those that do.
var (
	f009NoPriceTypes = []string{"", "movie", "tv_show"}
	f009PriceTypes   = []string{"motorcycle", "car", "camera", "book"}
)

// typeJSON is the JSON value of a list type; "" is a list with no type.
func typeJSON(typ string) string {
	if typ == "" {
		return "null"
	}
	return `"` + typ + `"`
}

func TestF009B1_PriceOnlyOnPriceTypes(t *testing.T) {
	e := newTreeEnv(t)
	lists := map[string]string{}
	for _, typ := range append(append([]string{}, f009PriceTypes...), f009NoPriceTypes...) {
		lists[typ] = e.listOfType("L-"+typ, nil, typ)["id"].(string)
	}

	t.Run("create with a price is 422 on the types that have none", func(t *testing.T) {
		for _, typ := range f009NoPriceTypes {
			p := wantProblem(t, e.call("POST", "/api/v1/lists/"+lists[typ]+"/items", priceBody, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "price" || p.Errors[0].Message != "items of this type have no price" {
				t.Errorf("%s: errors = %+v", typ, p.Errors)
			}
			if rows, _ := e.page(lists[typ], ""); len(rows) != 0 {
				t.Errorf("%s: rejected request created %d items", typ, len(rows))
			}
		}
	})

	t.Run("create with a price is fine on the types that have one", func(t *testing.T) {
		for _, typ := range f009PriceTypes {
			it := e.newItem(lists[typ], priceBody)
			if p := it["price"].(map[string]any); p["min"] != "12.00" || p["max"] != "12.00" || p["currency"] != "EUR" {
				t.Errorf("%s: price = %v", typ, p)
			}
		}
	})

	t.Run("create with price null is fine everywhere", func(t *testing.T) {
		for _, typ := range f009NoPriceTypes {
			wantStatus(t, e.call("POST", "/api/v1/lists/"+lists[typ]+"/items", `{"title":"t","price":null}`, ""), 201)
		}
	})

	t.Run("patch: a price is rejected without one, accepted with one", func(t *testing.T) {
		for _, typ := range f009NoPriceTypes {
			it := e.titled(lists[typ], "plain")
			p := wantProblem(t, e.patchItem(it, `{"price":{"min":"1.00","max":"2.00","currency":"EUR"}}`), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "price" {
				t.Errorf("%s: errors = %+v", typ, p.Errors)
			}
			if got := e.fetchItem(it["id"]); got["version"] != float64(1) || got["price"] != nil {
				t.Errorf("%s: rejected patch changed the item: %v", typ, got)
			}
			wantStatus(t, e.patchItem(it, `{"price":null}`), 200)
		}
		bike := e.newItem(lists["motorcycle"], priceBody)
		wantStatus(t, e.patchItem(bike, `{"price":{"amount":"13.00","currency":"EUR"}}`), 200)
	})
}

func TestF009B1_PriceTypesPinsAndSort(t *testing.T) {
	e := newTreeEnv(t)

	t.Run("price is not a pinnable key without a price", func(t *testing.T) {
		for _, typ := range f009NoPriceTypes {
			p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"P","type":`+typeJSON(typ)+`,"pinned_fields":["price"]}`, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "pinned_fields[0]" {
				t.Errorf("%s: errors = %+v", typ, p.Errors)
			}
		}
		wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"B","type":"motorcycle","pinned_fields":["price"]}`, ""), 201)
		// An empty pin list stays fine on a type that has no price.
		g := e.listOfType("G", nil, "")
		wantStatus(t, e.call("PATCH", "/api/v1/lists/"+g["id"].(string), `{"pinned_fields":[]}`, etagOf("l", g)), 200)
	})

	t.Run("sort_mode price is rejected without a price", func(t *testing.T) {
		for _, typ := range f009NoPriceTypes {
			p := wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"S","type":`+typeJSON(typ)+`,"sort_mode":"price"}`, ""), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "sort_mode" {
				t.Errorf("%s: errors = %+v", typ, p.Errors)
			}
		}
		wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"M","type":"motorcycle","sort_mode":"price"}`, ""), 201)
	})

	t.Run("?sort=price is 400 invalid_parameter without a price", func(t *testing.T) {
		movie := e.listOfType("Films", nil, "movie")["id"].(string)
		e.titled(movie, "x")
		wantProblem(t, e.call("GET", "/api/v1/lists/"+movie+"/items?sort=price", "", ""), 400, "invalid_parameter")
		bike := e.listOfType("Bikes", nil, "motorcycle")["id"].(string)
		e.titled(bike, "x")
		wantStatus(t, e.call("GET", "/api/v1/lists/"+bike+"/items?sort=price", "", ""), 200)
	})
}

func TestF009B1_TypeChangeToTypeWithoutPriceClearsPrice(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Bikes", nil, "motorcycle")
	lid := l["id"].(string)
	priced := e.newItem(lid, priceBody)
	plain := e.titled(lid, "plain")
	trashed := e.newItem(lid, priceBody)
	other := e.newItem(e.listOfType("Other", nil, "motorcycle")["id"].(string), priceBody)
	e.exec(`UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, trashed["id"], e.trashBatch("item", uuid.MustParse(trashed["id"].(string))))

	l = decode(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"price","pinned_fields":["model_year","price"]}`, etagOf("l", l)))
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["type"] != "movie" || got["sort_mode"] != "release_year" || got["pinned_fields"] != nil {
		t.Fatalf("list after type change: %v", got)
	}
	if got["version"].(float64) <= l["version"].(float64) || rec.Header().Get("ETag") != etagOf("l", got) {
		t.Fatalf("version/ETag not bumped: %v %s", got, rec.Header().Get("ETag"))
	}
	if it := e.fetchItem(priced["id"]); it["price"] != nil || it["version"] != float64(2) {
		t.Fatalf("priced item after type change: %v", it)
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

// The price rule follows the definition's allows_price, not the built-in keys.
func TestF014_PriceFollowsAllowsPriceOnCustomTypes(t *testing.T) {
	e := newTreeEnv(t)
	e.exec(`INSERT INTO type_definitions (workspace_id, key, name, allows_price) VALUES ($1, 'priced', 'Priced', true), ($1, 'free', 'Free', false)`, e.ws)
	priced := e.listOfType("Priced", nil, "priced")["id"].(string)
	free := e.listOfType("Free", nil, "free")["id"].(string)
	wantStatus(t, e.call("POST", "/api/v1/lists/"+priced+"/items", priceBody, ""), 201)
	wantProblem(t, e.call("POST", "/api/v1/lists/"+free+"/items", priceBody, ""), 422, "validation_failed")
}
