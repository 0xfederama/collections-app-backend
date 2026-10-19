package httpapi_test

import (
	"testing"
)

// F018: only the title is ever required. Each built-in type creates an item
// with just a title (201), with no lookup and no price. Movie, TV and book
// items use plain create, so no TMDB or Open Library call is made.
func titleOnlyCreate(t *testing.T, typ string) {
	t.Helper()
	e := newTreeEnv(t)
	lid := e.listOfType("Only title "+typ, nil, typ)["id"].(string)
	rec := e.call("POST", "/api/v1/lists/"+lid+"/items", `{"title":"Only a title"}`, "")
	wantStatus(t, rec, 201)
	it := decode(t, rec)
	if it["title"] != "Only a title" || it["type"] != typ || it["price"] != nil {
		t.Fatalf("%s item = %v", typ, it)
	}
}

func TestF018_TitleOnly_movie(t *testing.T)    { titleOnlyCreate(t, "movie") }
func TestF018_TitleOnly_tv_show(t *testing.T)  { titleOnlyCreate(t, "tv_show") }
func TestF018_TitleOnly_book(t *testing.T)     { titleOnlyCreate(t, "book") }
func TestF018_TitleOnly_vehicle(t *testing.T)  { titleOnlyCreate(t, "vehicle") }
func TestF018_TitleOnly_wishlist(t *testing.T) { titleOnlyCreate(t, "wishlist") }
