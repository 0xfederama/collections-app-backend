package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/identity"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/users"
)

// lateIdentity runs the REAL identity middleware (dev identities a, b, c @test.dev) and maps the
// X-Test-User header of the shared test env onto its X-Dev-User header, so these tests go through
// token-less dev resolution, user creation and scope exactly as a dev server does.
type lateIdentity struct{ r *identity.Resolver }

func (l *lateIdentity) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get(testUserHeader); u != "" {
			r.Header.Del(testUserHeader)
			r.Header.Set("X-Dev-User", u+"@test.dev")
		}
		l.r.Middleware(next).ServeHTTP(w, r)
	})
}

// newAuthzEnv is the accounts env over the real identity middleware, acting as "a".
func newAuthzEnv(t *testing.T) *imageEnv {
	t.Helper()
	li := &lateIdentity{}
	e := newImageEnvAs(t, li)
	for name, id := range testUsers {
		insertUser(t, e.pool, e.ws, id, name+"@test.dev")
	}
	li.r = identity.New(identity.Options{
		Users: users.NewService(users.NewPostgres(e.pool), clock.System{}), Logger: quietLogger(),
		DevIdentities: []string{"a@test.dev", "b@test.dev", "c@test.dev"},
	})
	e.exec("INSERT INTO workspace_settings (workspace_id) VALUES ($1) ON CONFLICT DO NOTHING", e.ws)
	return e.as("a")
}

// box is everything one owner keeps under one parent: the targets of the matrices.
type box struct {
	folders, lists, items []string // ids; folders[0] is the first level, [1] nested in it
	attachment            string
	batches               []string // trash batch ids: a list, an item and a folder
	ids, titles           []string // every id and title, to prove nothing leaks
	itemURL               string
}

// buildBox creates, as e, a folder F (+ N inside), lists L1 (in parent) and L2 (in F), an item
// with an image and two content versions in each list, and three trash batches, all under parent
// (nil = the top level, a private box).
func (e *imageEnv) buildBox(tag string, parent any) box {
	e.t.Helper()
	var b box
	add := func(o obj, title string) string {
		id := str(o, "id")
		b.ids = append(b.ids, id)
		if title != "" {
			b.titles = append(b.titles, title)
		}
		return id
	}
	f := e.folder(tag+" folder", parent)
	n := e.folder(tag+" nested folder", f["id"])
	l1 := e.list(tag+" list", parent)
	l2 := e.list(tag+" nested list", f["id"])
	i1 := e.titled(str(l1, "id"), tag+" Alpha item")
	i2 := e.titled(str(l2, "id"), tag+" Alpha nested item")
	b.folders = []string{add(f, tag+" folder"), add(n, tag+" nested folder")}
	b.lists = []string{add(l1, tag+" list"), add(l2, tag+" nested list")}
	b.items = []string{add(i1, tag+" Alpha item"), add(i2, tag+" Alpha nested item")}
	b.attachment = attachmentIDOf(e.t, e.upload(i1, "a.png", pngFixture(e.t, 4, 4)))
	b.ids = append(b.ids, b.attachment)
	for _, id := range b.items {
		url := "/api/v1/items/" + id + "/content"
		e.save(url, 0, tag+" secret one")
		e.save(url, 1, tag+" secret two")
	}
	b.itemURL = "/api/v1/items/" + b.items[0]

	tl := e.list(tag+" trashed list", parent)
	til := e.list(tag+" list of the trashed item", parent)
	ti := e.titled(str(til, "id"), tag+" trashed item")
	tf := e.folder(tag+" trashed folder", parent)
	e.list(tag+" in trashed folder", tf["id"])
	e.mustDel("lists", "l", tl)
	e.mustDel("items", "i", ti)
	e.mustDel("folders", "f", tf)
	for _, root := range []obj{tl, ti, tf} {
		b.batches = append(b.batches, add(e.entry(root["id"]), ""))
	}
	b.ids = append(b.ids, str(tl, "id"), str(til, "id"), str(ti, "id"), str(tf, "id"))
	b.titles = append(b.titles, tag+" trashed list", tag+" trashed item", tag+" trashed folder")
	return b
}

func (b box) targets(kind string) []string {
	switch kind {
	case "folder":
		return b.folders
	case "list":
		return b.lists
	case "item":
		return b.items
	case "attachment":
		return []string{b.attachment}
	case "trash":
		return b.batches
	}
	panic("unknown kind " + kind)
}

// snapshot is the bytes of everything readable about the box as its owner (resources, lists of
// items, content, versions, files, tree, trash). Equal before and after means untouched.
func (e *imageEnv) snapshot(b box) string {
	e.t.Helper()
	var sb strings.Builder
	get := func(url string) {
		rec := e.call("GET", url, "", "")
		fmt.Fprintf(&sb, "%s %d %s\n", url, rec.Code, rec.Body)
	}
	for _, id := range b.folders {
		get("/api/v1/folders/" + id)
	}
	for _, id := range b.lists {
		get("/api/v1/lists/" + id)
		get("/api/v1/lists/" + id + "/items")
	}
	for _, id := range b.items {
		get("/api/v1/items/" + id)
		get("/api/v1/items/" + id + "/content")
		get("/api/v1/items/" + id + "/content/versions")
		get("/api/v1/items/" + id + "/content/versions/1")
	}
	get("/api/v1/attachments/" + b.attachment + "/file")
	get("/api/v1/attachments/" + b.attachment + "/thumbnail")
	get("/api/v1/tree")
	get("/api/v1/trash")
	return sb.String()
}

// pathCase is one resource-by-path operation: how to call it with a valid body and ETag so that
// a missing guard would really change something. etag is "res" (the resource's), "content" or "".
type pathCase struct {
	op, kind, method, path, body, etag string
}

// {b_list} is a list the actor owns; {b_user} the actor's id.
var pathCases = []pathCase{
	{"getFolder", "folder", "GET", "/api/v1/folders/{id}", "", ""},
	{"updateFolder", "folder", "PATCH", "/api/v1/folders/{id}", `{"name":"hacked"}`, "res"},
	{"deleteFolder", "folder", "DELETE", "/api/v1/folders/{id}", "", "res"},
	{"moveFolder", "folder", "POST", "/api/v1/folders/{id}/move", `{"parent_folder_id":null}`, "res"},
	{"addFolderMember", "folder", "POST", "/api/v1/folders/{id}/members", `{"email":"{b_email}"}`, ""},
	{"removeFolderMember", "folder", "DELETE", "/api/v1/folders/{id}/members/{user_id}", "", ""},
	{"getList", "list", "GET", "/api/v1/lists/{id}", "", ""},
	{"updateList", "list", "PATCH", "/api/v1/lists/{id}", `{"name":"hacked"}`, "res"},
	{"deleteList", "list", "DELETE", "/api/v1/lists/{id}", "", "res"},
	{"moveList", "list", "POST", "/api/v1/lists/{id}/move", `{"folder_id":null}`, "res"},
	{"listItems", "list", "GET", "/api/v1/lists/{id}/items", "", ""},
	{"createItem", "list", "POST", "/api/v1/lists/{id}/items", `{"title":"hacked"}`, ""},
	{"reorderListItems", "list", "PUT", "/api/v1/lists/{id}/items/order", `{"ids":[]}`, ""},
	{"refreshListLookups", "list", "POST", "/api/v1/lists/{id}/lookup-refresh", "", ""},
	{"getItem", "item", "GET", "/api/v1/items/{id}", "", ""},
	{"updateItem", "item", "PATCH", "/api/v1/items/{id}", `{"title":"hacked"}`, "res"},
	{"deleteItem", "item", "DELETE", "/api/v1/items/{id}", "", "res"},
	{"moveItem", "item", "POST", "/api/v1/items/{id}/move", `{"list_id":"{b_list}"}`, "res"},
	{"getItemContent", "item", "GET", "/api/v1/items/{id}/content", "", ""},
	{"updateItemContent", "item", "PUT", "/api/v1/items/{id}/content", `{"body":"hacked"}`, "content"},
	{"restoreContentVersion", "item", "POST", "/api/v1/items/{id}/content/restore", `{"version":1}`, "content"},
	{"listContentVersions", "item", "GET", "/api/v1/items/{id}/content/versions", "", ""},
	{"getContentVersion", "item", "GET", "/api/v1/items/{id}/content/versions/{version}", "", ""},
	{"setItemImage", "item", "PUT", "/api/v1/items/{id}/image", `{"url":"https://example.test/x.png"}`, "res"},
	{"deleteItemImage", "item", "DELETE", "/api/v1/items/{id}/image", "", "res"},
	{"lookupItemMetadata", "item", "POST", "/api/v1/items/{id}/lookup", `{}`, "res"},
	{"getAttachmentFile", "attachment", "GET", "/api/v1/attachments/{id}/file", "", ""},
	{"getAttachmentThumbnail", "attachment", "GET", "/api/v1/attachments/{id}/thumbnail", "", ""},
	{"purgeTrashBatch", "trash", "DELETE", "/api/v1/trash/{batch_id}", "", ""},
	{"restoreTrashBatch", "trash", "POST", "/api/v1/trash/{batch_id}/restore", `{"destination":{"folder_id":null}}`, ""},
}

// etagFor reads the current ETag of a target as its owner.
func (e *imageEnv) etagFor(kind, id, etag string) string {
	e.t.Helper()
	switch etag {
	case "res":
		prefix := map[string]string{"folder": "f", "list": "l", "item": "i"}[kind]
		return etagOf(prefix, e.fetch(map[string]string{"folder": "folders", "list": "lists", "item": "items"}[kind], id))
	case "content":
		rec := e.call("GET", "/api/v1/items/"+id+"/content", "", "")
		wantStatus(e.t, rec, 200)
		return fmt.Sprintf(`"c-%v"`, decode(e.t, rec)["version"])
	}
	return ""
}

var pathReplacer = func(id string, extra map[string]string) *strings.Replacer {
	pairs := []string{"{id}", id, "{batch_id}", id, "{version}", "1"}
	for k, v := range extra {
		pairs = append(pairs, k, v)
	}
	return strings.NewReplacer(pairs...)
}

// TestF027B6_PathCasesMatchTheSpec: the table has exactly the resource-by-path and share
// operations, with their real method and path.
func TestF027B6_PathCasesMatchTheSpec(t *testing.T) {
	spec := map[string]specOp{}
	for _, op := range specOps() {
		spec[op.id] = op
	}
	seen := map[string]bool{}
	for _, c := range pathCases {
		op, ok := spec[c.op]
		if !ok || op.method != c.method || op.path != c.path {
			t.Errorf("%s: case %s %s does not match the spec (%+v)", c.op, c.method, c.path, op)
		}
		if class := authzOps[c.op].class; class != clsPath && class != clsShare {
			t.Errorf("%s: class %q in the path table", c.op, class)
		}
		seen[c.op] = true
	}
	for id, c := range authzOps {
		if (c.class == clsPath || c.class == clsShare) && !seen[id] {
			t.Errorf("%s (%s) has no path case", id, c.class)
		}
	}
	for _, c := range bodyCases {
		if !authzOps[c.op].body {
			t.Errorf("%s: body case for an operation not marked body in authzOps", c.op)
		}
		seen["body:"+c.op] = true
	}
	for id, c := range authzOps {
		if c.body && !seen["body:"+id] {
			t.Errorf("%s is marked body but has no body case", id)
		}
	}
}

// runPathMatrix calls every resource-by-path operation, on every target of the box, as the actor
// (who must not see the box) and expects 404 not_found each time.
func runPathMatrix(t *testing.T, actor, owner *imageEnv, bx box, actorName string) {
	t.Helper()
	extra := map[string]string{"{user_id}": testUsers[actorName].String(), "{b_email}": actorName + "@test.dev"}
	bl := actor.list("actor's own list "+actorName, nil)
	extra["{b_list}"] = str(bl, "id")
	before := owner.snapshot(bx)
	for _, c := range pathCases {
		for _, id := range bx.targets(c.kind) {
			t.Run(c.op+"/"+id[:8], func(t *testing.T) {
				r := pathReplacer(id, extra)
				rec := actor.call(c.method, r.Replace(c.path), r.Replace(c.body), owner.etagFor(c.kind, id, c.etag))
				wantProblem(t, rec, 404, "not_found")
			})
		}
	}
	if after := owner.snapshot(bx); after != before {
		t.Errorf("the owner's data changed while %s was refused", actorName)
	}
	if got := actor.fetch("lists", str(bl, "id")); got["version"] != bl["version"] {
		t.Errorf("the actor's own list changed: %v", got)
	}
}

func TestF027B6_PrivateBoxIsInvisibleToOtherUsersOnEveryPathOperation(t *testing.T) {
	e := newAuthzEnv(t)
	bx := e.buildBox("A", nil)
	for _, who := range []string{"b", "c"} {
		t.Run("as "+who, func(t *testing.T) { runPathMatrix(t, e.as(who), e, bx, who) })
	}
	// The owner still reaches everything the matrix tried (the answers above are not blanket 404s).
	for _, url := range []string{
		"/api/v1/folders/" + bx.folders[0], "/api/v1/lists/" + bx.lists[1], bx.itemURL, bx.itemURL + "/content",
		bx.itemURL + "/content/versions/1", "/api/v1/attachments/" + bx.attachment + "/file",
		"/api/v1/attachments/" + bx.attachment + "/thumbnail",
	} {
		wantStatus(t, e.call("GET", url, "", ""), 200)
	}
}

// bodyCase is an operation that names a resource in its body. The actor targets a destination
// they cannot see ({dest}); the answer is the same code whether it is a folder/list of someone
// else's box or one that does not exist.
type bodyCase struct {
	op, method, path, body, etag string
	dest                         string // "folder" or "list": which of the box's ids {dest} is
	status                       int
	code                         string
}

// {b_folder}, {b_list}, {b_item}, {b_batch_list}, {b_batch_item} belong to the actor.
var bodyCases = []bodyCase{
	{"createFolder", "POST", "/api/v1/folders", `{"name":"x","parent_folder_id":"{dest}"}`, "", "folder", 409, "destination_unavailable"},
	{"createList", "POST", "/api/v1/lists", `{"name":"x","folder_id":"{dest}"}`, "", "folder", 409, "destination_unavailable"},
	{"moveFolder", "POST", "/api/v1/folders/{b_folder}/move", `{"parent_folder_id":"{dest}"}`, "f", "folder", 409, "destination_unavailable"},
	{"moveList", "POST", "/api/v1/lists/{b_list}/move", `{"folder_id":"{dest}"}`, "l", "folder", 409, "destination_unavailable"},
	{"moveItem", "POST", "/api/v1/items/{b_item}/move", `{"list_id":"{dest}"}`, "i", "list", 409, "destination_unavailable"},
	{"restoreTrashBatch", "POST", "/api/v1/trash/{b_batch_list}/restore", `{"destination":{"folder_id":"{dest}"}}`, "", "folder", 409, "destination_unavailable"},
	{"restoreTrashBatch", "POST", "/api/v1/trash/{b_batch_item}/restore", `{"destination":{"list_id":"{dest}"}}`, "", "list", 409, "destination_unavailable"},
	{"reorderFolders", "PUT", "/api/v1/folders/order", `{"parent_folder_id":"{dest}","ids":[]}`, "", "folder", 404, "not_found"},
	{"reorderLists", "PUT", "/api/v1/lists/order", `{"folder_id":"{dest}","ids":[]}`, "", "folder", 404, "not_found"},
	{"reorderListItems", "PUT", "/api/v1/lists/{b_list}/items/order", `{"ids":["{dest}"]}`, "", "item", 409, "order_stale"},
}

func TestF027B6_BodyReferencesToAnotherUsersBoxChangeNothing(t *testing.T) {
	e := newAuthzEnv(t)
	bx := e.buildBox("A", nil)
	b := e.as("b")
	fb := b.folder("B folder", nil)
	lb := b.list("B list", nil)
	ib := b.titled(str(lb, "id"), "B item")
	tl := b.list("B trashed list", nil)
	til := b.list("B list of trashed item", nil)
	ti := b.titled(str(til, "id"), "B trashed item")
	b.mustDel("lists", "l", tl)
	b.mustDel("items", "i", ti)
	repl := map[string]string{
		"{b_folder}": str(fb, "id"), "{b_list}": str(lb, "id"), "{b_item}": str(ib, "id"),
		"{b_batch_list}": str(b.entry(tl["id"]), "id"), "{b_batch_item}": str(b.entry(ti["id"]), "id"),
	}
	prefix := map[string]struct{ typ, prefix, id string }{
		"f": {"folders", "f", str(fb, "id")}, "l": {"lists", "l", str(lb, "id")}, "i": {"items", "i", str(ib, "id")},
	}
	before := e.snapshot(bx)
	bBefore := b.snapshotOwn(fb, lb, ib)
	for _, c := range bodyCases {
		var dests []string
		switch c.dest {
		case "folder":
			dests = bx.folders
		case "list":
			dests = bx.lists
		case "item":
			dests = bx.items
		}
		// A destination that does not exist is the same answer as one that is not visible.
		for _, d := range append(append([]string{}, dests...), "00000000-0000-4000-8000-00000000dead") {
			t.Run(fmt.Sprintf("%s/%s/%s", c.op, strings.TrimPrefix(c.path, "/api/v1/"), d[:8]), func(t *testing.T) {
				etag := ""
				if p, ok := prefix[c.etag]; ok {
					etag = etagOf(p.prefix, b.fetch(p.typ, p.id))
				}
				r := strings.NewReplacer("{dest}", d)
				pr := strings.NewReplacer(flat(repl)...)
				rec := b.call(c.method, pr.Replace(c.path), pr.Replace(r.Replace(c.body)), etag)
				wantProblem(t, rec, c.status, c.code)
			})
		}
	}
	if after := e.snapshot(bx); after != before {
		t.Error("the owner's data changed")
	}
	if after := b.snapshotOwn(fb, lb, ib); after != bBefore {
		t.Errorf("the actor's own data moved:\n%s\n--\n%s", bBefore, after)
	}
}

func flat(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}

func (e *imageEnv) snapshotOwn(f, l, i obj) string {
	e.t.Helper()
	var sb strings.Builder
	for _, u := range []string{"/folders/" + str(f, "id"), "/lists/" + str(l, "id"), "/items/" + str(i, "id"), "/trash", "/tree"} {
		rec := e.call("GET", "/api/v1"+u, "", "")
		fmt.Fprintf(&sb, "%s %d %s\n", u, rec.Code, rec.Body)
	}
	return sb.String()
}

func TestF027B6_CollectionsNeverContainAnotherUsersPrivateRows(t *testing.T) {
	e := newAuthzEnv(t)
	bx := e.buildBox("A", nil)
	b := e.as("b")
	fb := b.folder("Bravo folder", nil)
	lb := b.list("Bravo list", fb["id"])
	b.titled(str(lb, "id"), "Alpha Bravo mine")
	tb := b.list("Bravo trashed", nil)
	b.mustDel("lists", "l", tb)

	leak := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		wantStatus(t, rec, 200)
		for _, s := range append(append([]string{}, bx.ids...), bx.titles...) {
			if strings.Contains(rec.Body.String(), s) {
				t.Errorf("%s of b contains %q: %s", what, s, rec.Body)
			}
		}
	}
	leak("tree", b.call("GET", "/api/v1/tree", "", ""))
	leak("search", b.call("GET", "/api/v1/search?q=alpha", "", ""))
	leak("search by secret", b.call("GET", "/api/v1/search?q=secret", "", ""))
	leak("recent", b.call("GET", "/api/v1/recent-items", "", ""))
	leak("trash", b.call("GET", "/api/v1/trash", "", ""))
	if got := b.search(q("alpha")).titles(); len(got) != 1 || got[0] != "Alpha Bravo mine" {
		t.Errorf("b search = %v", got)
	}
	if got := b.recent("").titles(); len(got) != 1 {
		t.Errorf("b recent = %v", got)
	}
	if entries, _ := b.trashPage(""); len(entries) != 1 || entries[0]["root_id"] != tb["id"] {
		t.Errorf("b trash = %v", entries)
	}
	// C owns nothing: every collection is empty.
	c := e.as("c")
	if len(c.tree("folders")) != 0 || len(c.tree("lists")) != 0 || len(c.search(q("alpha")).Data) != 0 || len(c.recent("").Data) != 0 {
		t.Error("c sees rows of others")
	}
	if entries, _ := c.trashPage(""); len(entries) != 0 {
		t.Errorf("c trash = %v", entries)
	}

	// Emptying the trash purges only what the caller sees.
	before := e.snapshot(bx)
	wantStatus(t, b.call("DELETE", "/api/v1/trash", "", ""), 204)
	wantStatus(t, c.call("DELETE", "/api/v1/trash", "", ""), 204)
	if after := e.snapshot(bx); after != before {
		t.Error("another user's empty-trash touched a's data")
	}
	b.noEntry(tb["id"])
	// And A still sees all of A's.
	if got := e.search(q("alpha")).titles(); len(got) != 2 {
		t.Errorf("a search = %v", got)
	}
	if entries, _ := e.trashPage(""); len(entries) != 3 {
		t.Errorf("a trash = %d entries", len(entries))
	}
}
