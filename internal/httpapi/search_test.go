package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// searchRows is one page of GET /search.
type searchRows struct {
	Data []struct {
		Item     obj   `json:"item"`
		TypeName any   `json:"type_name"`
		Path     []obj `json:"path"`
	}
	Page obj
}

func TestF014B5_CustomTypeListsAndSearchNames(t *testing.T) {
	e := newTreeEnv(t)
	created := e.call("POST", "/api/v1/types", `{"name":"Board game"}`, "")
	wantStatus(t, created, 201)
	typ := decode(t, created)
	key := typ["key"].(string)
	list := e.call("POST", "/api/v1/lists", fmt.Sprintf(`{"name":"Shelf","type":%q}`, key), "")
	wantStatus(t, list, 201)
	lid := decode(t, list)["id"].(string)
	e.newItem(lid, `{"title":"Catan"}`)

	got := e.search(q("catan"))
	if len(got.Data) != 1 || got.Data[0].Item["type"] != key || got.Data[0].TypeName != "Board game" {
		t.Fatalf("custom-type search result = %+v", got.Data)
	}
	if definition := e.typ(key); definition["name"] != "Board game" {
		t.Fatalf("custom type missing from definitions picker data: %v", definition)
	}
	recent := e.call("GET", "/api/v1/recent-items", "", "")
	wantStatus(t, recent, 200)
	var recentResults searchRows
	if err := json.Unmarshal(recent.Body.Bytes(), &recentResults); err != nil {
		t.Fatal(err)
	}
	if len(recentResults.Data) != 1 || recentResults.Data[0].TypeName != "Board game" {
		t.Fatalf("custom-type recent result = %+v", recentResults.Data)
	}
}

func (e *treeEnv) search(query string) searchRows {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/search?"+query, "", "")
	wantStatus(e.t, rec, 200)
	var out searchRows
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (s searchRows) titles() []string {
	out := make([]string, 0, len(s.Data))
	for _, r := range s.Data {
		out = append(out, r.Item["title"].(string))
	}
	return out
}

func q(text string) string { return "q=" + url.QueryEscape(text) }

// trashedItem inserts an item that is in the trash under any list (as the batch cascade leaves it).
func (e *treeEnv) trashedItem(listID, title string) {
	e.t.Helper()
	batch := e.trashBatch("item", uuid.New())
	e.exec(`INSERT INTO items (workspace_id, list_id, title, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, 0, now(), $4)`,
		e.ws, listID, title, batch)
}

func TestAT12_SearchSubstringAndPath(t *testing.T) {
	e := newTreeEnv(t)
	media := e.folder("Media", nil)
	movies := e.folder("Movies", media["id"])
	watchlist := e.listOfType("Watchlist", movies["id"], "movie")
	rootList := e.list("Inbox", nil)

	e.newItem(watchlist["id"].(string), matrix)
	e.titled(rootList["id"].(string), "Matte paint")
	e.titled(rootList["id"].(string), "Unrelated")

	for _, text := range []string{"MAT", "mat", "  mat  ", "atri"} {
		got := e.search(q(text))
		want := []string{"The Matrix"}
		if text != "atri" {
			want = []string{"Matte paint", "The Matrix"}
		}
		if !slices.Equal(got.titles(), want) {
			t.Fatalf("q=%q: titles = %v, want %v", text, got.titles(), want)
		}
	}

	got := e.search(q("matrix"))
	if len(got.Data) != 1 {
		t.Fatalf("matrix: %d results", len(got.Data))
	}
	hit := got.Data[0]
	wantPath := []obj{
		{"type": "folder", "id": media["id"], "name": "Media", "icon": nil, "icon_color": nil},
		{"type": "folder", "id": movies["id"], "name": "Movies", "icon": nil, "icon_color": nil},
		{"type": "list", "id": watchlist["id"], "name": "Watchlist", "icon": nil, "icon_color": nil},
	}
	if !reflect.DeepEqual(hit.Path, wantPath) {
		t.Fatalf("nested path = %v, want %v", hit.Path, wantPath)
	}
	// Summary shape: same fields as a list row, incl. metadata.
	for _, k := range []string{"id", "list_id", "title", "type", "image", "price", "website", "metadata", "content_version", "sort_order", "version", "created_at", "updated_at"} {
		if _, ok := hit.Item[k]; !ok {
			t.Errorf("item summary lacks %q", k)
		}
	}
	if hit.Item["type"] != "movie" || got.Data[0].TypeName != "Movies" || hit.Item["website"] != "https://www.warnerbros.com/movies/matrix" || hit.Item["image"] != nil {
		t.Errorf("unexpected summary: %v", hit.Item)
	}

	rootHit := e.search(q("matte")).Data[0]
	if rootHit.Item["type"] != nil {
		t.Errorf("TestF007 type of an item in a list with no type = %v", rootHit.Item["type"])
	}
	if want := []obj{{"type": "list", "id": rootList["id"], "name": "Inbox", "icon": nil, "icon_color": nil}}; !reflect.DeepEqual(rootHit.Path, want) {
		t.Fatalf("root list path = %v, want %v", rootHit.Path, want)
	}

	none := e.search(q("zzz"))
	if none.Data == nil || len(none.Data) != 0 || none.Page["total"].(float64) != 0 {
		t.Fatalf("no match should be an empty array, got %+v", none)
	}
}

func TestAT12_SearchWildcardsAreLiteral(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)["id"].(string)
	for _, title := range []string{"100% Pure", "50 percent", "a_b", "axb", `back\slash`, "plain"} {
		e.titled(l, title)
	}
	for text, want := range map[string][]string{
		"%":   {"100% Pure"},
		"_":   {"a_b"},
		"a_b": {"a_b"},
		`\`:   {`back\slash`},
		`\s`:  {`back\slash`},
		"%%":  nil,
	} {
		if got := e.search(q(text)).titles(); !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Errorf("q=%q: titles = %v, want %v", text, got, want)
		}
	}
}

func TestAT12_SearchExcludesTrash(t *testing.T) {
	e := newTreeEnv(t)
	active := e.list("Active", nil)["id"].(string)
	e.titled(active, "Needle active")
	e.trashedItem(active, "Needle trashed item")

	trashedList := e.trashList("Gone", nil)
	e.trashedItem(trashedList, "Needle in trashed list")

	trashedFolder := e.trashFolder("GoneFolder", nil)
	listInFolder := uuid.New()
	batch := e.trashBatch("list", listInFolder)
	e.exec(`INSERT INTO lists (id, workspace_id, folder_id, name, type, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, 'In gone folder', NULL, 0, now(), $4)`,
		listInFolder, e.ws, trashedFolder, batch)
	e.trashedItem(listInFolder.String(), "Needle in trashed folder")

	got := e.search(q("needle"))
	if want := []string{"Needle active"}; !slices.Equal(got.titles(), want) || got.Page["total"].(float64) != 1 {
		t.Fatalf("titles = %v total = %v, want %v and 1", got.titles(), got.Page["total"], want)
	}
}

func TestAT12_SearchRanksExactFirst(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)["id"].(string)
	for _, title := range []string{"The Matrix", "Matrix Reloaded", "Matrix", "Zion Matrix", "matrix"} {
		e.titled(l, title)
	}
	got := e.search(q("MATRIX")).titles()
	// Exact (any case) first, ordered by lower(title) then id, then the rest alphabetically.
	isExact := func(s string) bool { return s == "Matrix" || s == "matrix" }
	if len(got) != 5 || !isExact(got[0]) || !isExact(got[1]) || got[0] == got[1] {
		t.Fatalf("exact matches should lead: %v", got)
	}
	if want := []string{"Matrix Reloaded", "The Matrix", "Zion Matrix"}; !slices.Equal(got[2:], want) {
		t.Fatalf("rest = %v, want %v", got[2:], want)
	}
}

func TestAT12_SearchPagination(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)["id"].(string)
	for i := 1; i <= 5; i++ {
		e.titled(l, fmt.Sprintf("Item %d", i))
	}
	e.titled(l, "Other")

	cases := []struct {
		query     string
		want      []string
		limit, of float64
	}{
		{q("item"), []string{"Item 1", "Item 2", "Item 3", "Item 4", "Item 5"}, 50, 0},
		{q("item") + "&limit=2", []string{"Item 1", "Item 2"}, 2, 0},
		{q("item") + "&limit=2&offset=2", []string{"Item 3", "Item 4"}, 2, 2},
		{q("item") + "&limit=2&offset=4", []string{"Item 5"}, 2, 4},
		{q("item") + "&limit=2&offset=99", []string{}, 2, 99},
		{q("item") + "&limit=200", []string{"Item 1", "Item 2", "Item 3", "Item 4", "Item 5"}, 200, 0},
	}
	for _, c := range cases {
		got := e.search(c.query)
		if !slices.Equal(got.titles(), c.want) {
			t.Errorf("%s: titles = %v, want %v", c.query, got.titles(), c.want)
		}
		if got.Page["total"].(float64) != 5 || got.Page["limit"].(float64) != c.limit || got.Page["offset"].(float64) != c.of {
			t.Errorf("%s: page = %v, want total 5 limit %v offset %v", c.query, got.Page, c.limit, c.of)
		}
	}
}

func TestAT12_SearchRejectsBadQuery(t *testing.T) {
	e := newTreeEnv(t)
	for name, query := range map[string]string{
		"missing":    "limit=5",
		"empty":      "q=",
		"whitespace": "q=%20%20",
		"too long":   q(fmt.Sprintf("%0201d", 0)),
		"bad limit":  q("a") + "&limit=201",
	} {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, e.call("GET", "/api/v1/search?"+query, "", ""), 400, "invalid_parameter")
		})
	}
}
