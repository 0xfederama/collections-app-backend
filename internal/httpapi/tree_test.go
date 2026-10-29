package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/seed"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

// treeEnv is a router on a fresh migrated database with one workspace.
type treeEnv struct {
	t    *testing.T
	h    http.Handler
	pool *pgxpool.Pool
	ws   uuid.UUID
}

func newTreeEnv(t *testing.T) *treeEnv {
	t.Helper()
	ctx := t.Context()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	if err := seed.BuiltinTypes(ctx, sqlc.New(pool), ws); err != nil {
		t.Fatal(err)
	}
	h := newRouter(t, httpapi.Deps{
		DB: pool, DataDir: t.TempDir(), Logger: quietLogger(), WorkspaceID: ws,
		Definitions: definitions.New(pool, ws), Tree: tree.New(pool, clock.System{}, ws), Items: items.New(pool, clock.System{}, ws),
		Search: search.New(pool, ws),
	})
	return &treeEnv{t: t, h: h, pool: pool, ws: ws}
}

func (e *treeEnv) call(method, path, body string, ifMatch string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := req{method: method, path: path, body: body}
	if body != "" {
		r.contentType = jsonCT
	}
	if ifMatch != "" {
		r.headers = map[string]string{"If-Match": ifMatch}
	}
	return do(e.t, e.h, r)
}

func (e *treeEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.t.Context(), sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

type obj map[string]any

func decode(t *testing.T, rec *httptest.ResponseRecorder) obj {
	t.Helper()
	var o obj
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("body is not a JSON object: %v: %s", err, rec.Body)
	}
	return o
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body)
	}
}

func (e *treeEnv) folder(name string, parent any) obj {
	e.t.Helper()
	body, _ := json.Marshal(obj{"name": name, "parent_folder_id": parent})
	rec := e.call("POST", "/api/v1/folders", string(body), "")
	wantStatus(e.t, rec, 201)
	f := decode(e.t, rec)
	if rec.Header().Get("Location") != "/api/v1/folders/"+f["id"].(string) || rec.Header().Get("ETag") != fmt.Sprintf(`"f-%v"`, f["version"]) {
		e.t.Fatalf("bad Location/ETag: %v / %v", rec.Header().Get("Location"), rec.Header().Get("ETag"))
	}
	return f
}

// list creates a list with no type.
func (e *treeEnv) list(name string, folder any) obj {
	e.t.Helper()
	return e.listOfType(name, folder, "")
}

// listOfType creates a list of the given type; "" is a list with no type (type null).
func (e *treeEnv) listOfType(name string, folder any, typ string) obj {
	e.t.Helper()
	var typeValue any
	if typ != "" {
		typeValue = typ
	}
	body, _ := json.Marshal(obj{"name": name, "type": typeValue, "folder_id": folder})
	rec := e.call("POST", "/api/v1/lists", string(body), "")
	wantStatus(e.t, rec, 201)
	l := decode(e.t, rec)
	if rec.Header().Get("Location") != "/api/v1/lists/"+l["id"].(string) || rec.Header().Get("ETag") != fmt.Sprintf(`"l-%v"`, l["version"]) {
		e.t.Fatalf("bad Location/ETag: %v / %v", rec.Header().Get("Location"), rec.Header().Get("ETag"))
	}
	if l["type"] != typeValue {
		e.t.Fatalf("type = %v, want %v", l["type"], typeValue)
	}
	return l
}

func (e *treeEnv) fetch(typ, id string) obj {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/"+typ+"/"+id, "", "")
	wantStatus(e.t, rec, 200)
	return decode(e.t, rec)
}

// tree returns the (type) entries of GET /tree in response order.
func (e *treeEnv) tree(typ string) []obj {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/tree", "", "")
	wantStatus(e.t, rec, 200)
	var raw map[string][]obj
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		e.t.Fatal(err)
	}
	return raw[typ]
}

// names lists names for entries in order.
func names(os []obj) []string {
	out := make([]string, 0, len(os))
	for _, o := range os {
		out = append(out, o["name"].(string))
	}
	return out
}

func etagOf(prefix string, o obj) string { return fmt.Sprintf(`"%s-%v"`, prefix, o["version"]) }

// trashFolder / trashList / trashItem insert rows that are already in the trash, with their batch.
func (e *treeEnv) trashBatch(rootType string, rootID uuid.UUID) uuid.UUID {
	e.t.Helper()
	batch := uuid.New()
	e.exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, $2, $3, $4, 'x', '[]')`,
		batch, e.ws, rootType, rootID)
	return batch
}

func (e *treeEnv) trashFolder(name string, parent any) string {
	e.t.Helper()
	id := uuid.New()
	batch := e.trashBatch("folder", id)
	e.exec(`INSERT INTO folders (id, workspace_id, parent_folder_id, name, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, $4, 0, now(), $5)`,
		id, e.ws, parent, name, batch)
	return id.String()
}

func (e *treeEnv) trashList(name string, folder any) string {
	e.t.Helper()
	id := uuid.New()
	batch := e.trashBatch("list", id)
	e.exec(`INSERT INTO lists (id, workspace_id, folder_id, name, type, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, $4, NULL, 0, now(), $5)`,
		id, e.ws, folder, name, batch)
	return id.String()
}

func (e *treeEnv) item(listID string, trashed bool) {
	e.t.Helper()
	if !trashed {
		e.exec(`INSERT INTO items (workspace_id, list_id, title, sort_order) VALUES ($1, $2, 'i', 0)`, e.ws, listID)
		return
	}
	batch := e.trashBatch("item", uuid.New())
	e.exec(`INSERT INTO items (workspace_id, list_id, title, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, 'i', 0, now(), $3)`, e.ws, listID, batch)
}

func TestAT03_HierarchyMoveAndOrder(t *testing.T) {
	e := newTreeEnv(t)

	media := e.folder("Media", nil)
	books := e.folder("Books", nil)
	movies := e.folder("Movies", media["id"])
	if movies["parent_folder_id"] != media["id"] || movies["sort_order"].(float64) != 0 || books["sort_order"].(float64) != 1 {
		t.Fatalf("unexpected folders: media=%v books=%v movies=%v", media, books, movies)
	}
	if media["parent_folder_id"] != nil {
		t.Fatalf("root folder parent_folder_id = %v, want null", media["parent_folder_id"])
	}

	watch := e.list("Watchlist", movies["id"])
	loose := e.list("Loose", nil)
	if watch["display_mode"] != "thumbnail" || watch["sort_mode"] != "manual" || watch["description"] != nil || watch["item_count"].(float64) != 0 {
		t.Fatalf("list defaults wrong: %v", watch)
	}

	// Move the loose list root → Books → root: appended last each time, version bumped.
	extra := e.list("Extra", books["id"])
	rec := e.call("POST", "/api/v1/lists/"+loose["id"].(string)+"/move", fmt.Sprintf(`{"folder_id":%q}`, books["id"]), etagOf("l", loose))
	wantStatus(t, rec, 200)
	moved := decode(t, rec)
	if moved["folder_id"] != books["id"] || moved["sort_order"].(float64) != 1 || moved["version"].(float64) != 2 || rec.Header().Get("ETag") != `"l-2"` {
		t.Fatalf("move into folder: %v etag=%s", moved, rec.Header().Get("ETag"))
	}
	if extra["sort_order"].(float64) != 0 {
		t.Fatalf("extra sort_order = %v", extra["sort_order"])
	}
	rec = e.call("POST", "/api/v1/lists/"+loose["id"].(string)+"/move", `{"folder_id":null}`, etagOf("l", moved))
	wantStatus(t, rec, 200)
	if back := decode(t, rec); back["folder_id"] != nil || back["sort_order"].(float64) != 0 || back["version"].(float64) != 3 {
		t.Fatalf("move back to root: %v", back)
	}

	// Reorder root folders: Books before Media. sort_order is 0..n-1 and versions are untouched.
	rec = e.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q]}`, books["id"], media["id"]), "")
	wantStatus(t, rec, 204)
	if b := e.fetch("folders", books["id"].(string)); b["sort_order"].(float64) != 0 || b["version"].(float64) != 1 {
		t.Fatalf("books after reorder: %v", b)
	}
	if m := e.fetch("folders", media["id"].(string)); m["sort_order"].(float64) != 1 || m["version"].(float64) != 1 {
		t.Fatalf("media after reorder: %v", m)
	}

	// Reorder lists in Books: [moved-away loose is gone, only Extra]; and a two-list folder.
	second := e.list("Second", books["id"])
	rec = e.call("PUT", "/api/v1/lists/order", fmt.Sprintf(`{"folder_id":%q,"ids":[%q,%q]}`, books["id"], second["id"], extra["id"]), "")
	wantStatus(t, rec, 204)
	if l := e.fetch("lists", second["id"].(string)); l["sort_order"].(float64) != 0 || l["version"].(float64) != 1 {
		t.Fatalf("second after reorder: %v", l)
	}
	if l := e.fetch("lists", extra["id"].(string)); l["sort_order"].(float64) != 1 {
		t.Fatalf("extra after reorder: %v", l)
	}

	// Move a folder to the root and under another: appended last.
	rec = e.call("POST", "/api/v1/folders/"+movies["id"].(string)+"/move", `{"parent_folder_id":null}`, etagOf("f", movies))
	wantStatus(t, rec, 200)
	if f := decode(t, rec); f["parent_folder_id"] != nil || f["sort_order"].(float64) != 2 || f["version"].(float64) != 2 {
		t.Fatalf("move folder to root: %v", f)
	}
}

func TestAT04_CyclesRejected(t *testing.T) {
	e := newTreeEnv(t)
	a := e.folder("A", nil)
	b := e.folder("B", a["id"])
	c := e.folder("C", b["id"])
	before := e.tree("folders")

	tests := []struct {
		name   string
		folder obj
		dest   string
	}{
		{"into itself", a, a["id"].(string)},
		{"into child", a, b["id"].(string)},
		{"into grandchild", a, c["id"].(string)},
		{"middle into its child", b, c["id"].(string)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.call("POST", "/api/v1/folders/"+tt.folder["id"].(string)+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, tt.dest), etagOf("f", tt.folder))
			wantProblem(t, rec, 409, "cycle_detected")
			after := e.tree("folders")
			bb, _ := json.Marshal(before)
			ab, _ := json.Marshal(after)
			if string(bb) != string(ab) {
				t.Fatalf("DB changed after rejected move:\nbefore %s\nafter  %s", bb, ab)
			}
		})
	}

	// A legal move still works: C to the root.
	rec := e.call("POST", "/api/v1/folders/"+c["id"].(string)+"/move", `{"parent_folder_id":null}`, etagOf("f", c))
	wantStatus(t, rec, 200)
}

func TestAT04_ConcurrentOppositeMovesCannotCreateCycle(t *testing.T) {
	e := newTreeEnv(t)
	a := e.folder("A", nil)
	b := e.folder("B", nil)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	moves := []struct {
		id, dest string
		v        obj
	}{{a["id"].(string), b["id"].(string), a}, {b["id"].(string), a["id"].(string), b}}
	for i, m := range moves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.call("POST", "/api/v1/folders/"+m.id+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, m.dest), etagOf("f", m.v))
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	if (codes[0] != 200 || codes[1] != 409) && (codes[0] != 409 || codes[1] != 200) {
		t.Fatalf("status codes = %v, want one 200 and one 409 cycle_detected", codes)
	}
}

func TestAT21_ReorderValidation(t *testing.T) {
	e := newTreeEnv(t)
	f1 := e.folder("F1", nil)
	f2 := e.folder("F2", nil)
	child := e.folder("Child", f1["id"])
	trashed := e.trashFolder("Gone", nil)
	other := uuid.NewString()

	l1 := e.list("L1", f1["id"])
	l2 := e.list("L2", f1["id"])
	otherList := e.list("Elsewhere", f2["id"])
	trashedList := e.trashList("GoneList", f1["id"])

	id := func(o obj) string { return o["id"].(string) }
	folderCases := []struct {
		name string
		ids  []string
	}{
		{"duplicate", []string{id(f1), id(f1)}},
		{"missing", []string{id(f1)}},
		{"extra unknown", []string{id(f1), id(f2), other}},
		{"foreign (other parent)", []string{id(f1), id(child)}},
		{"trashed", []string{id(f1), id(f2), trashed}},
		{"empty", []string{}},
	}
	for _, tt := range folderCases {
		t.Run("folders "+tt.name, func(t *testing.T) {
			body, _ := json.Marshal(obj{"parent_folder_id": nil, "ids": tt.ids})
			wantProblem(t, e.call("PUT", "/api/v1/folders/order", string(body), ""), 409, "order_stale")
		})
	}
	listCases := []struct {
		name string
		ids  []string
	}{
		{"duplicate", []string{id(l1), id(l1)}},
		{"missing", []string{id(l1)}},
		{"foreign (other folder)", []string{id(l1), id(otherList)}},
		{"trashed", []string{id(l1), id(l2), trashedList}},
		{"unknown", []string{id(l1), other}},
	}
	for _, tt := range listCases {
		t.Run("lists "+tt.name, func(t *testing.T) {
			body, _ := json.Marshal(obj{"folder_id": f1["id"], "ids": tt.ids})
			wantProblem(t, e.call("PUT", "/api/v1/lists/order", string(body), ""), 409, "order_stale")
		})
	}

	// Nothing above changed the order.
	if a, b := e.fetch("folders", id(f1)), e.fetch("folders", id(f2)); a["sort_order"].(float64) != 0 || b["sort_order"].(float64) != 1 {
		t.Fatalf("order changed by rejected reorders: %v %v", a["sort_order"], b["sort_order"])
	}

	// A valid reorder writes 0..n-1 (also over an order with gaps).
	e.exec("UPDATE folders SET sort_order = sort_order * 10 + 5 WHERE deleted_at IS NULL AND parent_folder_id IS NULL")
	body, _ := json.Marshal(obj{"parent_folder_id": nil, "ids": []string{id(f2), id(f1)}})
	wantStatus(t, e.call("PUT", "/api/v1/folders/order", string(body), ""), 204)
	if a, b := e.fetch("folders", id(f2)), e.fetch("folders", id(f1)); a["sort_order"].(float64) != 0 || b["sort_order"].(float64) != 1 {
		t.Fatalf("sort_order after reorder: %v %v", a["sort_order"], b["sort_order"])
	}
	// The empty set is valid for a parent without children.
	body, _ = json.Marshal(obj{"parent_folder_id": id(f2), "ids": []string{}})
	wantStatus(t, e.call("PUT", "/api/v1/folders/order", string(body), ""), 204)

	// Reorder under a missing or trashed parent: 404.
	for _, parent := range []string{other, trashed} {
		body, _ = json.Marshal(obj{"parent_folder_id": parent, "ids": []string{}})
		wantProblem(t, e.call("PUT", "/api/v1/folders/order", string(body), ""), 404, "not_found")
		body, _ = json.Marshal(obj{"folder_id": parent, "ids": []string{}})
		wantProblem(t, e.call("PUT", "/api/v1/lists/order", string(body), ""), 404, "not_found")
	}
}

func TestVersionConflicts(t *testing.T) {
	e := newTreeEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", nil)

	// Bump both once, so the original ETags are stale.
	rec := e.call("PATCH", "/api/v1/folders/"+f["id"].(string), `{"name":"F2"}`, etagOf("f", f))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["name"] != "F2" || got["version"].(float64) != 2 || rec.Header().Get("ETag") != `"f-2"` {
		t.Fatalf("rename: %v", got)
	}
	wantStatus(t, e.call("PATCH", "/api/v1/lists/"+l["id"].(string), `{"name":"L2"}`, etagOf("l", l)), 200)

	cases := []struct{ name, method, path, body, ifMatch string }{
		{"update folder", "PATCH", "/api/v1/folders/" + f["id"].(string), `{"name":"X"}`, `"f-1"`},
		{"move folder", "POST", "/api/v1/folders/" + f["id"].(string) + "/move", `{"parent_folder_id":null}`, `"f-1"`},
		{"update list", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"name":"X"}`, `"l-1"`},
		{"move list", "POST", "/api/v1/lists/" + l["id"].(string) + "/move", `{"folder_id":null}`, `"l-1"`},
		{"update folder, future version", "PATCH", "/api/v1/folders/" + f["id"].(string), `{"name":"X"}`, `"f-9"`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call(tt.method, tt.path, tt.body, tt.ifMatch), 412, "version_conflict")
			if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
				t.Fatalf("current_version = %v, want 2", p.CurrentVersion)
			}
		})
	}
	if got := e.fetch("folders", f["id"].(string)); got["name"] != "F2" || got["version"].(float64) != 2 {
		t.Fatalf("stale writes changed the folder: %v", got)
	}
}

func TestTrashedAndMissingAreNotFound(t *testing.T) {
	e := newTreeEnv(t)
	tf := e.trashFolder("T", nil)
	tl := e.trashList("TL", nil)
	missing := uuid.NewString()
	live := e.folder("Live", nil)

	for _, id := range []string{tf, missing} {
		wantProblem(t, e.call("GET", "/api/v1/folders/"+id, "", ""), 404, "not_found")
		wantProblem(t, e.call("PATCH", "/api/v1/folders/"+id, `{"name":"x"}`, `"f-1"`), 404, "not_found")
		wantProblem(t, e.call("POST", "/api/v1/folders/"+id+"/move", `{"parent_folder_id":null}`, `"f-1"`), 404, "not_found")
	}
	for _, id := range []string{tl, missing} {
		wantProblem(t, e.call("GET", "/api/v1/lists/"+id, "", ""), 404, "not_found")
		wantProblem(t, e.call("PATCH", "/api/v1/lists/"+id, `{"name":"x"}`, `"l-1"`), 404, "not_found")
		wantProblem(t, e.call("POST", "/api/v1/lists/"+id+"/move", `{"folder_id":null}`, `"l-1"`), 404, "not_found")
	}

	// Creating or moving into a missing or trashed destination: 409 destination_unavailable.
	l := e.list("L", nil)
	for _, dest := range []string{tf, missing} {
		wantProblem(t, e.call("POST", "/api/v1/folders", fmt.Sprintf(`{"name":"n","parent_folder_id":%q}`, dest), ""), 409, "destination_unavailable")
		wantProblem(t, e.call("POST", "/api/v1/lists", fmt.Sprintf(`{"name":"n","folder_id":%q}`, dest), ""), 409, "destination_unavailable")
		wantProblem(t, e.call("POST", "/api/v1/folders/"+live["id"].(string)+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, dest), etagOf("f", live)), 409, "destination_unavailable")
		wantProblem(t, e.call("POST", "/api/v1/lists/"+l["id"].(string)+"/move", fmt.Sprintf(`{"folder_id":%q}`, dest), etagOf("l", l)), 409, "destination_unavailable")
	}

	// The trash never shows in the tree.
	if f, ls := e.tree("folders"), e.tree("lists"); len(f) != 1 || len(ls) != 1 {
		t.Fatalf("tree = %v / %v, want only the live folder and list", f, ls)
	}
}

func TestGetListPathAndItemCount(t *testing.T) {
	e := newTreeEnv(t)
	media := e.folder("Media", nil)
	movies := e.folder("Movies", media["id"])
	l := e.list("Watchlist", movies["id"])
	root := e.list("Root", nil)

	e.item(l["id"].(string), false)
	e.item(l["id"].(string), false)
	e.item(l["id"].(string), true)

	got := e.fetch("lists", l["id"].(string))
	if got["item_count"].(float64) != 2 {
		t.Fatalf("item_count = %v, want 2 (trashed item excluded)", got["item_count"])
	}
	path, _ := json.Marshal(got["path"])
	want := fmt.Sprintf(`[{"id":%q,"name":"Media","type":"folder"},{"id":%q,"name":"Movies","type":"folder"}]`, media["id"], movies["id"])
	if string(path) != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	if p := e.fetch("lists", root["id"].(string))["path"]; p == nil || len(p.([]any)) != 0 {
		t.Fatalf("root list path = %v, want []", p)
	}
	for _, ls := range e.tree("lists") {
		if ls["id"] == l["id"] {
			if ls["item_count"].(float64) != 2 {
				t.Fatalf("tree item_count = %v, want 2", ls["item_count"])
			}
			if _, has := ls["path"]; has {
				t.Fatalf("tree lists must not carry path: %v", ls)
			}
		}
	}
	// A list in the tree is ordered (sort_order, id): Watchlist(0, in Movies) and Root(0, root) tie on sort_order.
	if len(e.tree("lists")) != 2 {
		t.Fatal("tree should list both lists")
	}
}

func TestUpdateListFields(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)
	path := "/api/v1/lists/" + l["id"].(string)

	rec := e.call("PATCH", path, `{"name":"  Renamed  ","description":"hello","display_mode":"grid","sort_mode":"title"}`, `"l-1"`)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["name"] != "Renamed" || got["description"] != "hello" || got["display_mode"] != "grid" || got["sort_mode"] != "title" || got["version"].(float64) != 2 {
		t.Fatalf("update: %v", got)
	}

	// Absent fields stay; explicit null clears the description.
	rec = e.call("PATCH", path, `{"description":null}`, `"l-2"`)
	wantStatus(t, rec, 200)
	got = decode(t, rec)
	if got["description"] != nil || got["name"] != "Renamed" || got["display_mode"] != "grid" || got["version"].(float64) != 3 {
		t.Fatalf("clear description: %v", got)
	}

	// An empty patch changes nothing and keeps the version.
	rec = e.call("PATCH", path, `{}`, `"l-3"`)
	wantStatus(t, rec, 200)
	if got = decode(t, rec); got["version"].(float64) != 3 || rec.Header().Get("ETag") != `"l-3"` {
		t.Fatalf("empty patch: %v", got)
	}
	// ...but a stale version still conflicts.
	wantProblem(t, e.call("PATCH", path, `{}`, `"l-1"`), 412, "version_conflict")
}

func TestFieldValidation(t *testing.T) {
	e := newTreeEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", nil)
	long := strings.Repeat("x", 201)

	cases := []struct {
		name, method, path, body, ifMatch string
		fields                            []string
	}{
		{"folder empty name", "POST", "/api/v1/folders", `{"name":""}`, "", []string{"name"}},
		{"folder blank name", "POST", "/api/v1/folders", `{"name":"   "}`, "", []string{"name"}},
		{"folder long name", "POST", "/api/v1/folders", `{"name":"` + long + `"}`, "", []string{"name"}},
		{"folder rename blank", "PATCH", "/api/v1/folders/" + f["id"].(string), `{"name":" "}`, `"f-1"`, []string{"name"}},
		{"list empty name", "POST", "/api/v1/lists", `{"name":""}`, "", []string{"name"}},
		{"list bad display mode", "POST", "/api/v1/lists", `{"name":"n","display_mode":"huge","sort_mode":"random"}`, "", []string{"display_mode"}},
		{"TestF007 list NUL in description", "POST", "/api/v1/lists", `{"name":"n","description":"a\u0000b"}`, "", []string{"description"}},
		{"list patch name and enum", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"name":"","display_mode":"x"}`, `"l-1"`, []string{"name", "display_mode"}},
		{"TestF007 list patch NUL in description", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"description":"a\u0000b"}`, `"l-1"`, []string{"description"}},
		{"TestF007 list unknown type", "POST", "/api/v1/lists", `{"name":"n","type":"unknown"}`, "", []string{"type"}},
		{"TestF007 list type_key on generic", "POST", "/api/v1/lists", `{"name":"n","sort_mode":"type_key"}`, "", []string{"sort_mode"}},
		{"TestF013B4 director sort on generic", "POST", "/api/v1/lists", `{"name":"n","sort_mode":"director"}`, "", []string{"sort_mode"}},
		{"TestF013B4 director sort on tv", "POST", "/api/v1/lists", `{"name":"n","type":"tv_show","sort_mode":"director"}`, "", []string{"sort_mode"}},
		{"TestF007 list patch unknown type", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"type":"unknown"}`, `"l-1"`, []string{"type"}},
		{"TestF007 list patch type_key on generic", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"sort_mode":"type_key"}`, `"l-1"`, []string{"sort_mode"}},
		{"TestF013B4 list patch director on generic", "PATCH", "/api/v1/lists/" + l["id"].(string), `{"sort_mode":"director"}`, `"l-1"`, []string{"sort_mode"}},
		{"folder move without parent_folder_id", "POST", "/api/v1/folders/" + f["id"].(string) + "/move", `{}`, `"f-1"`, []string{"parent_folder_id"}},
		{"list move without folder_id", "POST", "/api/v1/lists/" + l["id"].(string) + "/move", `{}`, `"l-1"`, []string{"folder_id"}},
		{"folder reorder without parent", "PUT", "/api/v1/folders/order", `{"ids":[]}`, "", []string{"parent_folder_id"}},
		{"list reorder without folder", "PUT", "/api/v1/lists/order", `{"ids":[]}`, "", []string{"folder_id"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, e.call(tt.method, tt.path, tt.body, tt.ifMatch), 422, "validation_failed")
			var got []string
			for _, fe := range p.Errors {
				got = append(got, fe.Field)
			}
			if strings.Join(got, ",") != strings.Join(tt.fields, ",") {
				t.Fatalf("error fields = %v, want %v", got, tt.fields)
			}
		})
	}

	// Multi-byte names count characters, not bytes: 200 runes pass, 201 fail.
	wantStatus(t, e.call("POST", "/api/v1/folders", `{"name":"`+strings.Repeat("é", 200)+`"}`, ""), 201)
	wantProblem(t, e.call("POST", "/api/v1/folders", `{"name":"`+strings.Repeat("é", 201)+`"}`, ""), 422, "validation_failed")

	// Names are stored trimmed.
	if got := e.folder("  Padded  ", nil); got["name"] != "Padded" {
		t.Fatalf("name = %q, want trimmed", got["name"])
	}
}

func TestTreeShape(t *testing.T) {
	e := newTreeEnv(t)
	rec := e.call("GET", "/api/v1/tree", "", "")
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != `{"folders":[],"lists":[]}` {
		t.Fatalf("empty tree = %s, want empty arrays", rec.Body)
	}

	a := e.folder("A", nil)
	e.folder("B", nil)
	e.list("L1", a["id"])
	e.list("L2", a["id"])
	e.list("L3", nil)
	if got := names(e.tree("folders")); strings.Join(got, ",") != "A,B" {
		t.Fatalf("folders = %v", got)
	}
	// Lists ordered by (sort_order, id): L1 and L3 tie at 0 (different parents), L2 is 1.
	got := e.tree("lists")
	if len(got) != 3 || got[2]["name"] != "L2" {
		t.Fatalf("lists = %v", names(got))
	}
}

func TestF007_ListTypeIsOptionalAndReturned(t *testing.T) {
	e := newTreeEnv(t)
	// No type (omitted or null) is a list with type null; a blank key is not.
	wantStatus(t, e.call("POST", "/api/v1/lists", `{"name":"n"}`, ""), 201)
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"n","type":""}`, ""), 422, "validation_failed")

	l := e.listOfType("Bikes", nil, "vehicle")
	if l["type"] != "vehicle" || e.fetch("lists", l["id"].(string))["type"] != "vehicle" {
		t.Fatalf("type not returned: %v", l)
	}
	found := false
	for _, tl := range e.tree("lists") {
		if tl["id"] == l["id"] {
			found = tl["type"] == "vehicle"
		}
	}
	if !found {
		t.Error("GET /tree must show the list type")
	}
	// Every built-in type can be created and uses its definition's default sort field.
	for _, typ := range []string{"", "movie", "tv_show", "vehicle", "wishlist", "book"} {
		e.listOfType("K "+typ, nil, typ)
	}
	for typ, field := range map[string]string{"movie": "release_year", "tv_show": "first_air_year", "vehicle": "manual", "wishlist": "manual", "book": "manual"} {
		rec := e.call("POST", "/api/v1/lists", fmt.Sprintf(`{"name":"S %s","type":%q}`, typ, typ), "")
		wantStatus(t, rec, 201)
		if decode(t, rec)["sort_mode"] != field {
			t.Errorf("%s: default sort = %v, want %s", typ, decode(t, rec)["sort_mode"], field)
		}
	}
	wantProblem(t, e.call("POST", "/api/v1/lists", `{"name":"S","sort_mode":"type_key"}`, ""), 422, "validation_failed")
}

func TestF007_ChangeListTypeClearsItemMetadata(t *testing.T) {
	e := newTreeEnv(t)
	l := e.listOfType("Films", nil, "movie")
	lid := l["id"].(string)
	it := e.newItem(lid, matrix)
	other := e.newItem(e.listOfType("Other films", nil, "movie")["id"].(string), matrix)
	// A trashed item of the list has its metadata cleared too.
	trashed := uuid.New()
	e.exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, 'gone', '{"release_year":1999}', 5, now(), $4)`,
		trashed, e.ws, lid, e.trashBatch("item", trashed))

	// The same type is a no-op: no bump.
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"] != l["version"] || got["type"] != "movie" {
		t.Fatalf("same type: %v", got)
	}
	if e.fetchItem(it["id"])["version"] != float64(1) {
		t.Fatal("same type touched the items")
	}

	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"vehicle"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	changed := decode(t, rec)
	if changed["type"] != "vehicle" || changed["version"].(float64) <= l["version"].(float64) || rec.Header().Get("ETag") != etagOf("l", changed) {
		t.Fatalf("after type change: %v etag=%s", changed, rec.Header().Get("ETag"))
	}
	got := e.fetchItem(it["id"])
	if got["type"] != "vehicle" || !reflect.DeepEqual(got["metadata"], map[string]any{}) || got["version"] != float64(2) || got["title"] != "The Matrix" {
		t.Fatalf("item after type change: %v", got)
	}
	if got["website"] != "https://www.warnerbros.com/movies/matrix" || got["price"] != nil {
		t.Errorf("only the type fields are cleared, got %v", got)
	}
	var meta string
	e.queryRow(`SELECT metadata::text FROM items WHERE id = $1`, []any{trashed}, &meta)
	if meta != "{}" {
		t.Errorf("trashed item metadata = %s, want {}", meta)
	}
	if o := e.fetchItem(other["id"]); o["type"] != "movie" || o["version"] != float64(1) || len(o["metadata"].(map[string]any)) == 0 {
		t.Errorf("an item of another list changed: %v", o)
	}

	// The item is validated against the new type from now on.
	wantProblem(t, e.patchItem(got, `{"metadata":{"release_year":1999}}`), 400, "unknown_field")
	wantStatus(t, e.patchItem(got, `{"metadata":{"make":"VW","model":"Golf"}}`), 200)
	got = e.fetchItem(it["id"])
	wantProblem(t, e.patchItem(got, `{"metadata":{"fuel_type":"electric"}}`), 400, "unknown_field")

	// A stale version still loses.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"wishlist"}`, etagOf("l", l)), 412, "version_conflict")
	// An unknown type is a 422 and changes nothing.
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"unknown"}`, etagOf("l", changed)), 422, "validation_failed")
	if cur := e.fetch("lists", lid); cur["type"] != "vehicle" || cur["version"] != changed["version"] {
		t.Fatalf("rejected patch changed the list: %v", cur)
	}
}

func TestF014B4_ListSortModeUsesFieldDefinitions(t *testing.T) {
	e := newTreeEnv(t)
	gen := e.list("Plain", nil)
	wantProblem(t, e.call("PATCH", "/api/v1/lists/"+gen["id"].(string), `{"sort_mode":"type_key"}`, etagOf("l", gen)), 422, "validation_failed")

	l := e.listOfType("Bikes", nil, "vehicle")
	lid := l["id"].(string)
	rec := e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"displacement_cc"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)
	if l["sort_mode"] != "displacement_cc" {
		t.Fatalf("sort_mode = %v", l["sort_mode"])
	}

	// Switching type takes the new type's default field sort (movie: release_year).
	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":"movie"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	l = decode(t, rec)
	if l["type"] != "movie" || l["sort_mode"] != "release_year" {
		t.Fatalf("after movie: %v", l)
	}

	rec = e.call("PATCH", "/api/v1/lists/"+lid, `{"type":null}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["type"] != nil || got["sort_mode"] != "manual" {
		t.Fatalf("got %v", got)
	}
	// Unknown definition keys remain invalid.
	cur := e.fetch("lists", lid)
	p := wantProblem(t, e.call("PATCH", "/api/v1/lists/"+lid, `{"sort_mode":"type_key"}`, etagOf("l", cur)), 422, "validation_failed")
	if len(p.Errors) != 1 || p.Errors[0].Field != "sort_mode" {
		t.Fatalf("errors = %+v, want one on sort_mode", p.Errors)
	}
}

// descEnv returns a tree service on the env's database and a fresh list with no type.
func (e *treeEnv) descEnv() (*tree.Service, tree.List) {
	e.t.Helper()
	svc := tree.New(e.pool, clock.System{}, e.ws)
	l, err := svc.CreateList(e.t.Context(), tree.CreateListInput{Name: "L"})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc, l
}

func TestF007_ListDescriptionLimitIsBytes(t *testing.T) {
	e := newTreeEnv(t)
	svc, l := e.descEnv()
	ctx := t.Context()
	patch := func(v *string) tree.ListPatch {
		return tree.ListPatch{Description: tree.Optional[string]{Set: true, Value: v}}
	}
	str := func(s string) *string { return &s }

	tests := []struct {
		name    string
		desc    string
		wantErr string // "", "413" or "422"
	}{
		{"exactly 65536 bytes", strings.Repeat("x", 65536), ""},
		{"65537 bytes", strings.Repeat("x", 65537), "413"},
		{"multi-byte counts bytes, not characters", strings.Repeat("é", 32769), "413"}, // 65538 bytes, 32769 runes
		{"multi-byte at the limit", strings.Repeat("é", 32768), ""},                    // 65536 bytes
		{"NUL", "a\x00b", "422"},
		{"invalid UTF-8", "a\xffb", "422"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, err := svc.GetList(ctx, l.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.UpdateList(ctx, l.ID, cur.Version, patch(str(tt.desc)))
			var tooLarge *apperr.PayloadTooLargeError
			var invalid *apperr.ValidationError
			switch tt.wantErr {
			case "":
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			case "413":
				if !errors.As(err, &tooLarge) || tooLarge.Limit != 65536 {
					t.Fatalf("err = %v, want PayloadTooLarge(65536)", err)
				}
			case "422":
				if !errors.As(err, &invalid) || invalid.Errors[0].Field != "description" {
					t.Fatalf("err = %v, want validation error on description", err)
				}
			}
		})
	}

	t.Run("create applies the same limit", func(t *testing.T) {
		_, err := svc.CreateList(ctx, tree.CreateListInput{Name: "N", Description: str(strings.Repeat("x", 65537))})
		var tooLarge *apperr.PayloadTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("err = %v, want PayloadTooLarge", err)
		}
		if _, err := svc.CreateList(ctx, tree.CreateListInput{Name: "N", Description: str(strings.Repeat("x", 65536))}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("over HTTP the stored markdown round-trips and 65537 bytes answer 413", func(t *testing.T) {
		path := "/api/v1/lists/" + l.ID.String()
		cur := e.fetch("lists", l.ID.String())
		rec := e.call("PATCH", path, `{"description":"# Title\n\n- one\n- two"}`, etagOf("l", cur))
		wantStatus(t, rec, 200)
		if got := decode(t, rec)["description"]; got != "# Title\n\n- one\n- two" {
			t.Fatalf("description = %q", got)
		}
		// The 64 KiB JSON body limit of the guard applies first, so the 413 is the same either way.
		cur = e.fetch("lists", l.ID.String())
		wantProblem(t, e.call("PATCH", path, `{"description":"`+strings.Repeat("x", 65537)+`"}`, etagOf("l", cur)), 413, "payload_too_large")
	})
}

func TestF007_ListDescriptionVersioning(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)
	path := "/api/v1/lists/" + l["id"].(string)

	// A change bumps the version.
	rec := e.call("PATCH", path, `{"description":"hello"}`, `"l-1"`)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["description"] != "hello" || got["version"].(float64) != 2 {
		t.Fatalf("edit: %v", got)
	}

	// The same description again changes nothing: no bump, same ETag.
	rec = e.call("PATCH", path, `{"description":"hello"}`, `"l-2"`)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["version"].(float64) != 2 || rec.Header().Get("ETag") != `"l-2"` {
		t.Fatalf("unchanged description bumped the version: %v / %s", got, rec.Header().Get("ETag"))
	}

	// A stale version conflicts, whatever the description.
	p := wantProblem(t, e.call("PATCH", path, `{"description":"other"}`, `"l-1"`), 412, "version_conflict")
	_ = p
	if got := e.fetch("lists", l["id"].(string)); got["description"] != "hello" || got["version"].(float64) != 2 {
		t.Fatalf("stale write changed the list: %v", got)
	}
	// A stale version conflicts even when the description is unchanged.
	wantProblem(t, e.call("PATCH", path, `{"description":"hello"}`, `"l-1"`), 412, "version_conflict")

	// Empty string and null both clear it.
	for _, body := range []string{`{"description":""}`, `{"description":null}`} {
		cur := e.fetch("lists", l["id"].(string))
		if cur["description"] == nil {
			wantStatus(t, e.call("PATCH", path, `{"description":"again"}`, etagOf("l", cur)), 200)
			cur = e.fetch("lists", l["id"].(string))
		}
		rec = e.call("PATCH", path, body, etagOf("l", cur))
		wantStatus(t, rec, 200)
		if got := decode(t, rec); got["description"] != nil || got["version"].(float64) != cur["version"].(float64)+1 {
			t.Fatalf("%s: %v", body, got)
		}
	}
}
