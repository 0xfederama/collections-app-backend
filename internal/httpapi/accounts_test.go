package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Ownership tests (F-027 B2): two users share one workspace, user "a" owns the seeded data and
// user "b" must not be able to see or touch any of it.

const testUserHeader = "X-Test-User"

var testUsers = map[string]uuid.UUID{
	"a": uuid.MustParse("00000000-0000-4000-8000-0000000000a1"),
	"b": uuid.MustParse("00000000-0000-4000-8000-0000000000b2"),
}

// headerIdentity acts as the user named by X-Test-User ("a" when absent).
type headerIdentity struct{}

func (headerIdentity) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.Header.Get(testUserHeader)
		if name == "" {
			name = "a"
		}
		id, ok := testUsers[name]
		if !ok {
			http.Error(w, "unknown test user", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(scope.WithUser(r.Context(), id)))
	})
}

// as returns the env acting as the named user.
func (e *imageEnv) as(user string) *imageEnv {
	c := *e
	tc := *e.treeEnv
	tc.user = user
	c.treeEnv = &tc
	return &c
}

func newAccountsEnv(t *testing.T) *imageEnv {
	t.Helper()
	e := newImageEnvAs(t, headerIdentity{})
	insertUser(t, e.pool, e.ws, testUsers["a"], "a@test.dev")
	insertUser(t, e.pool, e.ws, testUsers["b"], "b@test.dev")
	return e.as("a")
}

func str(o obj, key string) string { return o[key].(string) }

// seed is what user a owns: a private box of every kind.
type seed struct {
	topFolder, nestedFolder obj // FA, FA2 inside FA
	topList, nestedList     obj // LA, LA2 inside FA
	item, nestedItem        obj // in LA, LA2
	attachment              string
	batches                 map[string]string // trash batch ids by root kind
}

func (e *imageEnv) seedA(t *testing.T) seed {
	t.Helper()
	var s seed
	s.topFolder = e.folder("A box", nil)
	s.nestedFolder = e.folder("A nested", s.topFolder["id"])
	s.topList = e.list("A list", nil)
	s.nestedList = e.list("A nested list", s.topFolder["id"])
	s.item = e.titled(str(s.topList, "id"), "Alpha private")
	s.nestedItem = e.titled(str(s.nestedList, "id"), "Alpha nested private")
	withImage := e.upload(s.item, "a.png", pngFixture(t, 4, 4))
	s.attachment = attachmentIDOf(t, withImage)

	// Three trash batches: a list, an item and a folder with a list inside.
	trashList := e.list("A trashed list", nil)
	trashItemList := e.list("A list for the trashed item", nil)
	trashItem := e.titled(str(trashItemList, "id"), "A trashed item")
	trashFolder := e.folder("A trashed folder", nil)
	e.list("In trashed folder", trashFolder["id"])
	e.mustDel("lists", "l", trashList)
	e.mustDel("items", "i", trashItem)
	e.mustDel("folders", "f", trashFolder)
	s.batches = map[string]string{
		"list":   str(e.entry(trashList["id"]), "id"),
		"item":   str(e.entry(trashItem["id"]), "id"),
		"folder": str(e.entry(trashFolder["id"]), "id"),
	}
	return s
}

func TestF027_PathGuard_OtherUsersResourceIs404OnEveryPathParamOperation(t *testing.T) {
	e := newAccountsEnv(t)
	s := e.seedA(t)
	b := e.as("b")

	idsByKind := map[string][]string{
		"folders":     {str(s.topFolder, "id"), str(s.nestedFolder, "id")},
		"lists":       {str(s.topList, "id"), str(s.nestedList, "id")},
		"items":       {str(s.item, "id"), str(s.nestedItem, "id")},
		"attachments": {s.attachment},
		"trash":       {s.batches["list"], s.batches["item"], s.batches["folder"]},
	}
	missing := uuid.NewString()

	spec := httpapi.API(httpapi.Deps{}).OpenAPI()
	checked := 0
	for path, item := range spec.Paths {
		if !strings.Contains(path, "{") {
			continue
		}
		kind := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")[0]
		ids, ok := idsByKind[kind]
		if !ok {
			t.Errorf("%s: the test has no resource kind %q", path, kind)
			continue
		}
		for method, op := range operationsOf(item) {
			for _, id := range append(slices.Clone(ids), missing) {
				url := substitute(path, id)
				rec := b.callRaw(method, url)
				wantProblem(t, rec, 404, "not_found")
				// The same answer as a resource that does not exist.
				if gone := b.callRaw(method, substitute(path, missing)); gone.Body.String() != rec.Body.String() && id != missing {
					t.Errorf("%s %s (%s): body differs from a missing resource\n got  %s\n want %s", method, url, op.OperationID, rec.Body, gone.Body)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no operation with a path parameter was checked")
	}

	// A's data is untouched by all of that, and A still reaches it.
	a := e.as("a")
	for _, url := range []string{
		"/api/v1/folders/" + str(s.topFolder, "id"), "/api/v1/lists/" + str(s.topList, "id"),
		"/api/v1/items/" + str(s.item, "id"), "/api/v1/lists/" + str(s.nestedList, "id") + "/items",
		"/api/v1/attachments/" + s.attachment + "/file", "/api/v1/attachments/" + s.attachment + "/thumbnail",
		"/api/v1/items/" + str(s.item, "id") + "/content",
	} {
		if rec := a.callRaw("GET", url); rec.Code != 200 {
			t.Errorf("A GET %s = %d, want 200: %s", url, rec.Code, rec.Body)
		}
	}
	entries, _ := a.trashPage("")
	if len(entries) != 3 {
		t.Errorf("A's trash lost batches: %d entries", len(entries))
	}
}

// operationsOf lists the operations of a path item by HTTP method.
func operationsOf(p *huma.PathItem) map[string]*huma.Operation {
	out := map[string]*huma.Operation{}
	for m, op := range map[string]*huma.Operation{
		http.MethodGet: p.Get, http.MethodPut: p.Put, http.MethodPost: p.Post,
		http.MethodDelete: p.Delete, http.MethodPatch: p.Patch,
	} {
		if op != nil {
			out[m] = op
		}
	}
	return out
}

// substitute fills every path parameter: the resource id, and 1 for the content version.
func substitute(path, id string) string {
	path = strings.ReplaceAll(path, "{id}", id)
	path = strings.ReplaceAll(path, "{batch_id}", id)
	return strings.ReplaceAll(path, "{version}", "1")
}

// callRaw sends a request with no body and no If-Match: the guard answers before either matters.
func (e *treeEnv) callRaw(method, url string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call(method, url, "", "")
}

func TestF027_FilteredCollections(t *testing.T) {
	e := newAccountsEnv(t)
	s := e.seedA(t)
	b := e.as("b")
	fb := b.folder("B box", nil)
	lb := b.list("B list", nil)
	ib := b.titled(str(lb, "id"), "Alpha public")
	b.mustDel("items", "i", b.titled(str(lb, "id"), "B trashed item"))

	ids := func(rows []obj) []string {
		var out []string
		for _, r := range rows {
			out = append(out, str(r, "id"))
		}
		sort.Strings(out)
		return out
	}
	sorted := func(v ...string) []string { sort.Strings(v); return v }

	t.Run("tree", func(t *testing.T) {
		if got, want := ids(b.tree("folders")), sorted(str(fb, "id")); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("B folders = %v, want %v", got, want)
		}
		if got, want := ids(b.tree("lists")), sorted(str(lb, "id")); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("B lists = %v, want %v", got, want)
		}
		aFolders := ids(e.tree("folders"))
		if !contains(aFolders, str(s.topFolder, "id")) || !contains(aFolders, str(s.nestedFolder, "id")) || contains(aFolders, str(fb, "id")) {
			t.Errorf("A folders = %v", aFolders)
		}
		for _, l := range e.tree("lists") {
			if str(l, "id") == str(lb, "id") {
				t.Errorf("A sees B's list")
			}
		}
	})

	t.Run("search and recent items", func(t *testing.T) {
		search := func(u *imageEnv) (titles []string, total float64) {
			rec := u.call("GET", "/api/v1/search?q=alpha", "", "")
			wantStatus(t, rec, 200)
			var out struct {
				Data []struct {
					Item struct {
						Title string `json:"title"`
					} `json:"item"`
				} `json:"data"`
				Page struct {
					Total float64 `json:"total"`
				} `json:"page"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, d := range out.Data {
				titles = append(titles, d.Item.Title)
			}
			return titles, out.Page.Total
		}
		bt, bTotal := search(b)
		if len(bt) != 1 || bt[0] != "Alpha public" || bTotal != 1 {
			t.Errorf("B search = %v total %v, want only its own item", bt, bTotal)
		}
		at, aTotal := search(e)
		if len(at) != 2 || aTotal != 2 || contains(at, "Alpha public") {
			t.Errorf("A search = %v total %v, want its two items", at, aTotal)
		}
		recent := func(u *imageEnv) []string {
			rec := u.call("GET", "/api/v1/recent-items?limit=50", "", "")
			wantStatus(t, rec, 200)
			var out struct {
				Data []struct {
					Item struct {
						ID string `json:"id"`
					} `json:"item"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range out.Data {
				got = append(got, d.Item.ID)
			}
			return got
		}
		if got := recent(b); len(got) != 1 || got[0] != str(ib, "id") {
			t.Errorf("B recent = %v", got)
		}
		if got := recent(e); contains(got, str(ib, "id")) || len(got) != 2 {
			t.Errorf("A recent = %v", got)
		}
	})

	t.Run("trash list and total", func(t *testing.T) {
		bEntries, bPage := b.trashPage("")
		if len(bEntries) != 1 || bPage["total"].(float64) != 1 {
			t.Errorf("B trash = %d entries, total %v; want 1", len(bEntries), bPage["total"])
		}
		aEntries, aPage := e.trashPage("?limit=2")
		if len(aEntries) != 2 || aPage["total"].(float64) != 3 {
			t.Errorf("A trash page = %d entries, total %v; want 2 of 3", len(aEntries), aPage["total"])
		}
	})

	t.Run("empty trash only removes the caller's batches", func(t *testing.T) {
		wantStatus(t, b.call("DELETE", "/api/v1/trash", "", ""), 204)
		if entries, _ := b.trashPage(""); len(entries) != 0 {
			t.Errorf("B trash after emptying = %d entries", len(entries))
		}
		if entries, _ := e.trashPage(""); len(entries) != 3 {
			t.Errorf("A trash after B emptied theirs = %d entries, want 3", len(entries))
		}
	})
}

func contains(s []string, v string) bool { return slices.Contains(s, v) }

func TestF027_BodyReferencesToInvisibleDestinationsAre409(t *testing.T) {
	e := newAccountsEnv(t)
	s := e.seedA(t)
	b := e.as("b")
	fb := b.folder("B box", nil)
	lb := b.list("B list", nil)
	ib := b.titled(str(lb, "id"), "B item")
	fa, la := str(s.topFolder, "id"), str(s.topList, "id")

	conflict := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		t.Run(name, func(t *testing.T) { wantProblem(t, rec, 409, "destination_unavailable") })
	}
	conflict("create folder", b.call("POST", "/api/v1/folders", fmt.Sprintf(`{"name":"x","parent_folder_id":%q}`, fa), ""))
	conflict("create list", b.call("POST", "/api/v1/lists", fmt.Sprintf(`{"name":"x","folder_id":%q}`, fa), ""))
	conflict("move folder", b.call("POST", "/api/v1/folders/"+str(fb, "id")+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, fa), etagOf("f", fb)))
	conflict("move list", b.call("POST", "/api/v1/lists/"+str(lb, "id")+"/move", fmt.Sprintf(`{"folder_id":%q}`, fa), etagOf("l", lb)))
	conflict("move item", b.call("POST", "/api/v1/items/"+str(ib, "id")+"/move", fmt.Sprintf(`{"list_id":%q}`, la), etagOf("i", ib)))

	// Same answer for a destination that does not exist.
	missing := uuid.NewString()
	conflict("create folder in a missing parent", b.call("POST", "/api/v1/folders", fmt.Sprintf(`{"name":"x","parent_folder_id":%q}`, missing), ""))

	// Restore with a destination B cannot see.
	trashed := b.list("B trashed", nil)
	b.mustDel("lists", "l", trashed)
	batch := str(b.entry(trashed["id"]), "id")
	conflict("restore into a folder", b.call("POST", "/api/v1/trash/"+batch+"/restore", fmt.Sprintf(`{"destination":{"folder_id":%q}}`, fa), ""))
	trashedItem := b.titled(str(lb, "id"), "B trashed item")
	b.mustDel("items", "i", trashedItem)
	itemBatch := str(b.entry(trashedItem["id"]), "id")
	conflict("restore an item into a list", b.call("POST", "/api/v1/trash/"+itemBatch+"/restore", fmt.Sprintf(`{"destination":{"list_id":%q}}`, la), ""))

	// Reordering under a parent B cannot see is a 404, like a missing parent.
	wantProblem(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":%q,"ids":[]}`, fa), ""), 404, "not_found")
	wantProblem(t, b.call("PUT", "/api/v1/lists/order", fmt.Sprintf(`{"folder_id":%q,"ids":[]}`, fa), ""), 404, "not_found")

	// Nothing of A's changed.
	if got := e.fetch("folders", fa); got["version"].(float64) != 1 {
		t.Errorf("A's folder changed: %v", got)
	}
}

func TestF027_OwnershipOnCreateMoveAndRestore(t *testing.T) {
	e := newAccountsEnv(t)
	a := testUsers["a"].String()

	owner := func(typ string, o obj) any { return e.fetch(typ, str(o, "id"))["owner_user_id"] }

	top := e.folder("Top", nil)
	nested := e.folder("Nested", top["id"])
	if top["owner_user_id"] != a || nested["owner_user_id"] != nil {
		t.Fatalf("create: top owner %v, nested owner %v; want %s and null", top["owner_user_id"], nested["owner_user_id"], a)
	}
	l := e.list("L", nil)
	inner := e.list("Inner", top["id"])
	if l["owner_user_id"] != a || inner["owner_user_id"] != nil {
		t.Fatalf("create list: %v / %v", l["owner_user_id"], inner["owner_user_id"])
	}

	t.Run("moving into a folder clears the owner, to the top level sets it", func(t *testing.T) {
		rec := e.call("POST", "/api/v1/lists/"+str(l, "id")+"/move", fmt.Sprintf(`{"folder_id":%q}`, str(top, "id")), etagOf("l", l))
		wantStatus(t, rec, 200)
		if got := decode(t, rec)["owner_user_id"]; got != nil {
			t.Errorf("list moved into a folder: owner %v, want null", got)
		}
		cur := e.fetch("lists", str(l, "id"))
		rec = e.call("POST", "/api/v1/lists/"+str(l, "id")+"/move", `{"folder_id":null}`, etagOf("l", cur))
		wantStatus(t, rec, 200)
		if got := decode(t, rec)["owner_user_id"]; got != a {
			t.Errorf("list moved to the top: owner %v, want %s", got, a)
		}

		rec = e.call("POST", "/api/v1/folders/"+str(nested, "id")+"/move", `{"parent_folder_id":null}`, etagOf("f", nested))
		wantStatus(t, rec, 200)
		moved := decode(t, rec)
		if moved["owner_user_id"] != a {
			t.Errorf("folder moved to the top: owner %v, want %s", moved["owner_user_id"], a)
		}
		rec = e.call("POST", "/api/v1/folders/"+str(nested, "id")+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, str(top, "id")), etagOf("f", moved))
		wantStatus(t, rec, 200)
		if got := decode(t, rec)["owner_user_id"]; got != nil {
			t.Errorf("folder moved into a folder: owner %v, want null", got)
		}
	})

	t.Run("restore in place keeps the stored owner", func(t *testing.T) {
		cur := e.fetch("folders", str(top, "id"))
		e.mustDel("folders", "f", cur)
		batch := str(e.entry(top["id"]), "id")
		wantStatus(t, e.call("POST", "/api/v1/trash/"+batch+"/restore", "", ""), 200)
		if got := owner("folders", top); got != a {
			t.Errorf("top folder restored in place: owner %v, want %s", got, a)
		}
		if got := owner("lists", inner); got != nil {
			t.Errorf("nested list after the folder came back: owner %v, want null", got)
		}
	})

	t.Run("restore with a destination applies the move rule", func(t *testing.T) {
		cur := e.fetch("lists", str(inner, "id"))
		e.mustDel("lists", "l", cur)
		batch := str(e.entry(inner["id"]), "id")
		wantStatus(t, e.call("POST", "/api/v1/trash/"+batch+"/restore", `{"destination":{"folder_id":null}}`, ""), 200)
		if got := owner("lists", inner); got != a {
			t.Errorf("nested list restored to the top: owner %v, want %s", got, a)
		}
		cur = e.fetch("lists", str(inner, "id"))
		e.mustDel("lists", "l", cur)
		batch = str(e.entry(inner["id"]), "id")
		wantStatus(t, e.call("POST", "/api/v1/trash/"+batch+"/restore", fmt.Sprintf(`{"destination":{"folder_id":%q}}`, str(top, "id")), ""), 200)
		if got := owner("lists", inner); got != nil {
			t.Errorf("top list restored into a folder: owner %v, want null", got)
		}
	})
}

func TestF027_TopLevelOrderIsPerUser(t *testing.T) {
	e := newAccountsEnv(t)
	b := e.as("b")
	f1, f2, f3 := e.folder("F1", nil), e.folder("F2", nil), e.folder("F3", nil)
	fb1, fb2 := b.folder("B1", nil), b.folder("B2", nil)
	l1, l2 := e.list("L1", nil), e.list("L2", nil)

	rootFolders := func(u *imageEnv) (order []string, positions []float64) {
		for _, f := range u.tree("folders") {
			if f["parent_folder_id"] == nil {
				order = append(order, str(f, "name"))
				positions = append(positions, f["sort_order"].(float64))
			}
		}
		return order, positions
	}
	reorder := func(u *imageEnv, path, key string, ids ...obj) *httptest.ResponseRecorder {
		list := make([]string, len(ids))
		for i, o := range ids {
			list[i] = fmt.Sprintf("%q", str(o, "id"))
		}
		return u.call("PUT", path, fmt.Sprintf(`{%q:null,"ids":[%s]}`, key, strings.Join(list, ",")), "")
	}

	wantStatus(t, reorder(e, "/api/v1/folders/order", "parent_folder_id", f3, f1, f2), 204)
	wantStatus(t, reorder(b, "/api/v1/folders/order", "parent_folder_id", fb2, fb1), 204)
	wantStatus(t, reorder(e, "/api/v1/lists/order", "folder_id", l2, l1), 204)

	if order, pos := rootFolders(e); fmt.Sprint(order) != "[F3 F1 F2]" || fmt.Sprint(pos) != "[0 1 2]" {
		t.Errorf("A's folders = %v at %v, want [F3 F1 F2] at [0 1 2]", order, pos)
	}
	if order, pos := rootFolders(b); fmt.Sprint(order) != "[B2 B1]" || fmt.Sprint(pos) != "[0 1]" {
		t.Errorf("B's folders = %v at %v, want [B2 B1] at [0 1]", order, pos)
	}
	var listOrder []string
	for _, l := range e.tree("lists") {
		listOrder = append(listOrder, str(l, "name"))
	}
	if fmt.Sprint(listOrder) != "[L2 L1]" {
		t.Errorf("A's lists = %v, want [L2 L1]", listOrder)
	}

	// The shared sort_order is untouched, and each user only has their own rows.
	if got := e.fetch("folders", str(f1, "id"))["sort_order"]; got != f1["sort_order"] {
		t.Errorf("shared sort_order changed: %v -> %v", f1["sort_order"], got)
	}
	count := func(user string) (n int) {
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_top_order WHERE user_id = $1`, testUsers[user]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if a, bb := count("a"), count("b"); a != 5 || bb != 2 {
		t.Errorf("user_top_order rows: a=%d b=%d, want 5 and 2", a, bb)
	}

	// A new top-level entry lands after the ordered ones; B's reorder did not move A's.
	f4 := e.folder("F4", nil)
	if order, _ := rootFolders(e); fmt.Sprint(order) != "[F3 F1 F2 F4]" {
		t.Errorf("A's folders after a create = %v", order)
	}

	t.Run("a root reorder naming another user's folder is stale", func(t *testing.T) {
		wantProblem(t, reorder(b, "/api/v1/folders/order", "parent_folder_id", fb1, fb2, f1), 409, "order_stale")
		wantProblem(t, reorder(b, "/api/v1/folders/order", "parent_folder_id", f1), 409, "order_stale")
		wantProblem(t, reorder(b, "/api/v1/lists/order", "folder_id", l1), 409, "order_stale")
		// A reorder that leaves one of the caller's own folders out is stale too.
		wantProblem(t, reorder(e, "/api/v1/folders/order", "parent_folder_id", f1, f2, f3), 409, "order_stale")
		wantStatus(t, reorder(e, "/api/v1/folders/order", "parent_folder_id", f1, f2, f3, f4), 204)
	})

	t.Run("purging a folder removes its order rows", func(t *testing.T) {
		cur := e.fetch("folders", str(f1, "id"))
		e.mustDel("folders", "f", cur)
		batch := str(e.entry(f1["id"]), "id")
		var before int
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_top_order WHERE entry_id = $1`, str(f1, "id")).Scan(&before); err != nil || before != 1 {
			t.Fatalf("rows of the trashed folder = %d (%v), want 1: a trashed folder keeps its position", before, err)
		}
		wantStatus(t, e.call("DELETE", "/api/v1/trash/"+batch, "", ""), 204)
		var after int
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_top_order WHERE entry_id = $1`, str(f1, "id")).Scan(&after); err != nil || after != 0 {
			t.Fatalf("rows of the purged folder = %d (%v), want 0", after, err)
		}
	})
}
