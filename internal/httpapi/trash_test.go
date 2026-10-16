package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// ---- helpers ----

// del sends DELETE with the resource's current ETag (kind: folders, lists, items).
func (e *imageEnv) del(kind, prefix string, o obj) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("DELETE", "/api/v1/"+kind+"/"+o["id"].(string), "", etagOf(prefix, o))
}

func (e *imageEnv) mustDel(kind, prefix string, o obj) {
	e.t.Helper()
	wantStatus(e.t, e.del(kind, prefix, o), 204)
}

// trashPage returns GET /trash (data and page).
func (e *imageEnv) trashPage(query string) (entries []obj, page obj) {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/trash"+query, "", "")
	wantStatus(e.t, rec, 200)
	var raw struct {
		Data []obj `json:"data"`
		Page obj   `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		e.t.Fatal(err)
	}
	return raw.Data, raw.Page
}

// entry returns the trash entry whose root is rootID, failing if there is none.
func (e *imageEnv) entry(rootID any) obj {
	e.t.Helper()
	entries, _ := e.trashPage("")
	for _, en := range entries {
		if en["root_id"] == rootID {
			return en
		}
	}
	e.t.Fatalf("no trash entry for root %v in %v", rootID, entries)
	return nil
}

func (e *imageEnv) noEntry(rootID any) {
	e.t.Helper()
	entries, _ := e.trashPage("")
	for _, en := range entries {
		if en["root_id"] == rootID {
			e.t.Fatalf("trash entry for %v still exists", rootID)
		}
	}
}

func (e *imageEnv) restore(entry obj, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("POST", "/api/v1/trash/"+entry["id"].(string)+"/restore", body, "")
}

func (e *imageEnv) purge(entry obj) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("DELETE", "/api/v1/trash/"+entry["id"].(string), "", "")
}

func (e *imageEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.t.Context(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *imageEnv) wantGone(kind string, o obj) {
	e.t.Helper()
	wantProblem(e.t, e.call("GET", "/api/v1/"+kind+"/"+o["id"].(string), "", ""), 404, "not_found")
}

func counts(en obj) [3]int {
	c := en["counts"].(map[string]any)
	return [3]int{int(c["folders"].(float64)), int(c["lists"].(float64)), int(c["items"].(float64))}
}

func (e *imageEnv) searchFinds(title string) bool {
	e.t.Helper()
	return len(e.search("q="+title).titles()) > 0
}

// ---- AT-11 ----

func TestAT11_DeleteFolderHidesSubtreeAndRestoreKeepsOlderBatches(t *testing.T) {
	e := newImageEnv(t)
	f := e.folder("Media", nil)
	other := e.folder("Other", nil)
	g := e.folder("Sub", f["id"])
	l1 := e.list("L1", g["id"])
	l2 := e.list("L2", f["id"])
	i1 := e.titled(l1["id"].(string), "Zebra One")
	i2 := e.titled(l2["id"].(string), "Zebra Two")
	i3 := e.titled(l1["id"].(string), "Zebra Three")
	i1 = e.upload(i1, "a.png", pngFixture(t, 10, 10))

	// i3 goes first: its own batch must survive the folder's later delete and restore.
	e.mustDel("items", "i", i3)
	e.clk.Advance(time.Minute) // the trash list is ordered by deleted_at
	e.mustDel("folders", "f", f)

	for kind, objs := range map[string][]obj{"folders": {f, g}, "lists": {l1, l2}, "items": {i1, i2, i3}} {
		for _, o := range objs {
			e.wantGone(kind, o)
		}
	}
	if names := names(e.tree("folders")); len(names) != 1 || names[0] != "Other" {
		t.Fatalf("tree folders = %v", names)
	}
	if len(e.tree("lists")) != 0 {
		t.Fatalf("tree lists = %v", e.tree("lists"))
	}
	if e.searchFinds("zebra") {
		t.Fatal("search still finds trashed items")
	}
	wantProblem(t, e.call("GET", "/api/v1/lists/"+l1["id"].(string)+"/items", "", ""), 404, "not_found")

	// Newest first: the folder batch, then the item batch.
	entries, page := e.trashPage("")
	if len(entries) != 2 || page["total"].(float64) != 2 || entries[0]["root_id"] != f["id"] || entries[1]["root_id"] != i3["id"] {
		t.Fatalf("trash = %v / %v", entries, page)
	}
	fe, ie := entries[0], entries[1]
	if fe["root_type"] != "folder" || fe["root_name"] != "Media" || counts(fe) != [3]int{2, 2, 2} || fe["restorable_in_place"] != true {
		t.Fatalf("folder entry = %v", fe)
	}
	if len(fe["former_path"].([]any)) != 0 {
		t.Fatalf("former_path = %v", fe["former_path"])
	}
	if counts(ie) != [3]int{0, 0, 1} || ie["restorable_in_place"] != false {
		t.Fatalf("item entry = %v", ie)
	}
	path := ie["former_path"].([]any)
	if len(path) != 3 || path[0].(map[string]any)["name"] != "Media" || path[1].(map[string]any)["name"] != "Sub" ||
		path[2].(map[string]any)["type"] != "list" || path[2].(map[string]any)["name"] != "L1" {
		t.Fatalf("item former_path = %v", path)
	}
	// Trash keeps the image file (D-064).
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, i1)+"/file", "", ""), 200)

	rec := e.restore(fe, "")
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["root_type"] != "folder" || got["root_id"] != f["id"] {
		t.Fatalf("restore result = %v", got)
	}
	e.noEntry(f["id"])
	back := e.fetch("folders", f["id"].(string))
	if back["version"].(float64) != f["version"].(float64)+1 {
		t.Fatalf("root version = %v, want %v", back["version"], f["version"].(float64)+1)
	}
	// Appended last among its siblings.
	var roots []obj
	for _, fo := range e.tree("folders") {
		if fo["parent_folder_id"] == nil {
			roots = append(roots, fo)
		}
	}
	if got := names(roots); fmt.Sprint(got) != "[Other Media]" {
		t.Fatalf("root folders = %v", got)
	}
	e.fetchItem(i1["id"])
	e.fetchItem(i2["id"])
	if !e.searchFinds("Zebra%20One") {
		t.Fatal("search does not find the restored item")
	}
	// i3 stays trashed in its own batch, and its list is back now.
	e.wantGone("items", i3)
	if en := e.entry(i3["id"]); en["restorable_in_place"] != true {
		t.Fatalf("item entry after folder restore = %v", en)
	}
	wantStatus(t, e.restore(e.entry(i3["id"]), ""), 200)
	e.fetchItem(i3["id"])
	_ = other
}

func TestAT11_RestoreWithTrashedParentNeedsDestination(t *testing.T) {
	e := newImageEnv(t)
	p := e.folder("Parent", nil)
	l := e.list("L", p["id"])
	it := e.titled(l["id"].(string), "Thing")

	e.mustDel("lists", "l", l)
	e.mustDel("folders", "f", p)
	en := e.entry(l["id"])
	if en["restorable_in_place"] != false {
		t.Fatalf("entry = %v", en)
	}

	p409 := wantProblem(t, e.restore(en, ""), 409, "parent_in_trash")
	if p409.BlockingParent == nil || p409.BlockingParent.Type != "folder" || p409.BlockingParent.ID != p["id"] || p409.BlockingParent.Name != "Parent" {
		t.Fatalf("blocking_parent = %+v", p409.BlockingParent)
	}
	// An unusable destination: the trashed parent itself, or one that never existed.
	wantProblem(t, e.restore(en, fmt.Sprintf(`{"destination":{"folder_id":%q}}`, p["id"])), 409, "destination_unavailable")
	wantProblem(t, e.restore(en, fmt.Sprintf(`{"destination":{"folder_id":%q}}`, uuid.NewString())), 409, "destination_unavailable")
	// Nothing changed by those failures.
	e.entry(l["id"])

	d := e.folder("Dest", nil)
	wantStatus(t, e.restore(en, fmt.Sprintf(`{"destination":{"folder_id":%q}}`, d["id"])), 200)
	got := e.fetch("lists", l["id"].(string))
	if got["folder_id"] != d["id"] || got["version"].(float64) != l["version"].(float64)+1 {
		t.Fatalf("restored list = %v", got)
	}
	e.fetchItem(it["id"]) // the list's item came along
}

func TestAT11_RestoreItemWithTrashedList(t *testing.T) {
	e := newImageEnv(t)
	l := e.list("Films", nil)
	dest := e.list("Other", nil)
	keep := e.titled(dest["id"].(string), "Already here")
	it := e.titled(l["id"].(string), "Heat")

	e.mustDel("items", "i", it)
	e.mustDel("lists", "l", l)
	en := e.entry(it["id"])
	p := wantProblem(t, e.restore(en, ""), 409, "parent_in_trash")
	if p.BlockingParent == nil || p.BlockingParent.Type != "list" || p.BlockingParent.ID != l["id"] || p.BlockingParent.Name != "Films" {
		t.Fatalf("blocking_parent = %+v", p.BlockingParent)
	}
	wantProblem(t, e.restore(en, fmt.Sprintf(`{"destination":{"list_id":%q}}`, l["id"])), 409, "destination_unavailable")

	wantStatus(t, e.restore(en, fmt.Sprintf(`{"destination":{"list_id":%q}}`, dest["id"])), 200)
	got := e.fetchItem(it["id"])
	if got["list_id"] != dest["id"] || got["version"].(float64) != it["version"].(float64)+1 {
		t.Fatalf("restored item = %v", got)
	}
	// Appended after the item already there.
	if keep["sort_order"].(float64) >= got["sort_order"].(float64) {
		t.Fatalf("sort_order %v is not after %v", got["sort_order"], keep["sort_order"])
	}
}

func TestAT11_RestoreCycleAndDestinationShape(t *testing.T) {
	e := newImageEnv(t)
	a := e.folder("A", nil)
	b := e.folder("B", a["id"])
	c := e.folder("C", b["id"])
	l := e.list("L", nil)

	e.mustDel("folders", "f", c) // older batch, nested in what follows
	e.mustDel("folders", "f", a) // trashes A and B
	en := e.entry(a["id"])

	// Into itself, a descendant of the batch, or a descendant trashed by an older batch: a cycle.
	for _, target := range []obj{a, b, c} {
		wantProblem(t, e.restore(en, fmt.Sprintf(`{"destination":{"folder_id":%q}}`, target["id"])), 409, "cycle_detected")
	}
	e.entry(a["id"])

	for name, tc := range map[string]struct{ body string }{
		"folder with list_id":       {fmt.Sprintf(`{"destination":{"list_id":%q}}`, l["id"])},
		"empty destination":         {`{"destination":{}}`},
		"folder_id and list_id":     {fmt.Sprintf(`{"destination":{"folder_id":null,"list_id":%q}}`, l["id"])},
		"unknown field in the body": {`{"destination":{"folder_id":null},"x":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.restore(en, tc.body)
			if rec.Code != 422 && rec.Code != 400 {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
			}
		})
	}
	// An item batch needs list_id.
	it := e.titled(l["id"].(string), "x")
	e.mustDel("items", "i", it)
	wantProblem(t, e.restore(e.entry(it["id"]), `{"destination":{"folder_id":null}}`), 422, "validation_failed")

	// To the root: A and B come back, C stays in the trash.
	wantStatus(t, e.restore(en, `{"destination":{"folder_id":null}}`), 200)
	e.fetch("folders", a["id"].(string))
	e.fetch("folders", b["id"].(string))
	e.wantGone("folders", c)
	// C restores in place now: its parent B is active again.
	if e.entry(c["id"])["restorable_in_place"] != true {
		t.Fatal("C should be restorable in place")
	}
	wantStatus(t, e.restore(e.entry(c["id"]), ""), 200)
	if got := e.fetch("folders", c["id"].(string)); got["parent_folder_id"] != b["id"] {
		t.Fatalf("C parent = %v", got["parent_folder_id"])
	}
}

func TestAT11_RestoreUnknownBatchAndPurgeUnknownBatch(t *testing.T) {
	e := newImageEnv(t)
	missing := obj{"id": uuid.NewString()}
	wantProblem(t, e.restore(missing, ""), 404, "not_found")
	wantProblem(t, e.purge(missing), 404, "not_found")
	wantProblem(t, e.call("POST", "/api/v1/trash/not-a-uuid/restore", "", ""), 400, "invalid_parameter")
}

func TestAT11_PurgeRemovesRowsAndFilesIncludingNestedBatches(t *testing.T) {
	e := newImageEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", f["id"])
	keep := e.newItemInNewList("Untouched")
	keep = e.upload(keep, "keep.png", pngFixture(t, 8, 8))
	a := e.upload(e.titled(l["id"].(string), "A"), "a.png", pngFixture(t, 10, 10))
	b := e.upload(e.titled(l["id"].(string), "B"), "b.jpg", jpegFixture(t, 10, 10))
	e.titled(l["id"].(string), "No image")
	if got := len(e.fileKeys()); got != 6 {
		t.Fatalf("files before = %d", got)
	}

	e.mustDel("items", "i", b) // nested older batch with its own image
	e.mustDel("folders", "f", f)
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 2 {
		t.Fatalf("batches = %d", n)
	}

	wantStatus(t, e.purge(e.entry(f["id"])), 204)
	// Only the untouched item and its list are left.
	for q, want := range map[string]int{"folders": 0, "lists": 1, "items": 1, "trash_batches": 0} {
		if n := e.count("SELECT count(*) FROM " + q); n != want {
			t.Fatalf("%s: %d rows, want %d", q, n, want)
		}
	}
	if n := e.attachmentCount(); n != 1 {
		t.Fatalf("attachments = %d, want 1 (the untouched item's)", n)
	}
	if got := len(e.fileKeys()); got != 2 {
		t.Fatalf("files after = %d, want 2: %v", got, e.fileKeys())
	}
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, a)+"/file", "", ""), 404, "not_found")
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, keep)+"/file", "", ""), 200)
	e.wantConsistent()
	wantProblem(t, e.purge(obj{"id": uuid.NewString()}), 404, "not_found")
}

func TestAT11_PurgeListAndItemRoots(t *testing.T) {
	e := newImageEnv(t)
	l := e.list("L", nil)
	a := e.upload(e.titled(l["id"].(string), "A"), "a.png", pngFixture(t, 10, 10))
	b := e.upload(e.titled(l["id"].(string), "B"), "b.png", pngFixture(t, 12, 12))

	e.mustDel("items", "i", a)
	wantStatus(t, e.purge(e.entry(a["id"])), 204)
	if e.attachmentCount() != 1 || len(e.fileKeys()) != 2 {
		t.Fatalf("after item purge: attachments=%d files=%d", e.attachmentCount(), len(e.fileKeys()))
	}
	e.fetch("lists", l["id"].(string))

	e.mustDel("lists", "l", l)
	wantStatus(t, e.purge(e.entry(l["id"])), 204)
	if e.attachmentCount() != 0 || len(e.fileKeys()) != 0 || e.count(`SELECT count(*) FROM items`) != 0 {
		t.Fatalf("after list purge: attachments=%d files=%d", e.attachmentCount(), len(e.fileKeys()))
	}
	_ = b
}

func TestAT11_PurgeSurvivesFailingFileDeleteAndSweeperCleansUp(t *testing.T) {
	e := newImageEnv(t)
	it := e.upload(e.newItemInNewList("Doomed"), "d.png", pngFixture(t, 10, 10))
	e.mustDel("items", "i", it)

	e.store.setFail(true)
	wantStatus(t, e.purge(e.entry(it["id"])), 204)
	// No database reference is left, only orphan files.
	if e.attachmentCount() != 0 || e.count(`SELECT count(*) FROM items`) != 0 || e.count(`SELECT count(*) FROM trash_batches`) != 0 {
		t.Fatal("rows left after purge")
	}
	if len(e.fileKeys()) != 2 {
		t.Fatalf("files = %v", e.fileKeys())
	}

	e.store.setFail(false)
	e.clk.Advance(2 * time.Hour)
	res, err := e.svc.Sweep(t.Context())
	if err != nil || res.Files != 2 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	e.wantNoFiles()
}

// ---- delete preconditions ----

func TestDeleteNeedsMatchingIfMatchAndAnActiveResource(t *testing.T) {
	e := newImageEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", nil)
	it := e.titled(e.list("L2", nil)["id"].(string), "I")

	batches := 0 // each subtest trashes one resource
	for _, tc := range []struct {
		kind, prefix string
		o            obj
	}{{"folders", "f", f}, {"lists", "l", l}, {"items", "i", it}} {
		t.Run(tc.kind, func(t *testing.T) {
			stale := e.call("DELETE", "/api/v1/"+tc.kind+"/"+tc.o["id"].(string), "", fmt.Sprintf(`"%s-%d"`, tc.prefix, 99))
			p := wantProblem(t, stale, 412, "version_conflict")
			if p.CurrentVersion == nil || *p.CurrentVersion != 1 {
				t.Fatalf("current_version = %v", p.CurrentVersion)
			}
			wantProblem(t, e.call("DELETE", "/api/v1/"+tc.kind+"/"+tc.o["id"].(string), "", ""), 428, "precondition_required")
			// A failed delete changes nothing.
			e.fetch(tc.kind, tc.o["id"].(string))
			if n := e.count(`SELECT count(*) FROM trash_batches`); n != batches {
				t.Fatalf("batches = %d, want %d", n, batches)
			}
			batches++
			wantStatus(t, e.del(tc.kind, tc.prefix, tc.o), 204)
			// Already trashed or never there: 404 even with a stale ETag.
			wantProblem(t, e.del(tc.kind, tc.prefix, tc.o), 404, "not_found")
			wantProblem(t, e.call("DELETE", "/api/v1/"+tc.kind+"/"+uuid.NewString(), "", `"`+tc.prefix+`-1"`), 404, "not_found")
		})
	}
}

func TestDeleteTrashedChildrenCannotBeChanged(t *testing.T) {
	e := newImageEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", f["id"])
	it := e.titled(l["id"].(string), "I")
	e.mustDel("folders", "f", f)

	wantProblem(t, e.del("lists", "l", l), 404, "not_found")
	wantProblem(t, e.del("items", "i", it), 404, "not_found")
	wantProblem(t, e.patchItem(it, `{"title":"x"}`), 404, "not_found")
	wantProblem(t, e.call("POST", "/api/v1/lists/"+l["id"].(string)+"/items", `{"title":"n"}`, ""), 404, "not_found")
	if e.count(`SELECT count(*) FROM trash_batches`) != 1 {
		t.Fatal("deleting trashed children must not create batches")
	}
}

func TestListTrashPaging(t *testing.T) {
	e := newImageEnv(t)
	var ids []any
	for i := range 3 {
		f := e.folder(fmt.Sprintf("F%d", i), nil)
		ids = append(ids, f["id"])
		e.mustDel("folders", "f", f)
		e.clk.Advance(time.Minute) // distinct deleted_at, the order is by it
	}
	entries, page := e.trashPage("?limit=2")
	if len(entries) != 2 || page["total"].(float64) != 3 || page["limit"].(float64) != 2 || page["offset"].(float64) != 0 {
		t.Fatalf("page 1 = %v / %v", entries, page)
	}
	if entries[0]["root_id"] != ids[2] || entries[1]["root_id"] != ids[1] {
		t.Fatalf("order = %v", entries)
	}
	entries, page = e.trashPage("?limit=2&offset=2")
	if len(entries) != 1 || entries[0]["root_id"] != ids[0] || page["total"].(float64) != 3 {
		t.Fatalf("page 2 = %v / %v", entries, page)
	}
	wantProblem(t, e.call("GET", "/api/v1/trash?limit=500", "", ""), 400, "invalid_parameter")
	if entries, page = e.trashPage(""); len(entries) != 3 || page["limit"].(float64) != 50 {
		t.Fatalf("default page = %v / %v", len(entries), page)
	}
}

// Delete and move of the same subtree run under the tree lock, one after the
// other: whichever wins, no active row is ever left under a trashed parent.
func TestDeleteAndMoveOfTheSameSubtreeAreSerialized(t *testing.T) {
	e := newImageEnv(t)
	for i := range 6 {
		f := e.folder(fmt.Sprintf("F%d", i), nil)
		g := e.folder("G", f["id"])
		l := e.list("L", g["id"])
		e.titled(l["id"].(string), "I")

		var wg sync.WaitGroup
		var del, mv int
		wg.Add(2)
		go func() {
			defer wg.Done()
			del = e.del("folders", "f", f).Code
		}()
		go func() {
			defer wg.Done()
			mv = e.call("POST", "/api/v1/folders/"+g["id"].(string)+"/move", `{"parent_folder_id":null}`, etagOf("f", g)).Code
		}()
		wg.Wait()
		if del != 204 || (mv != 200 && mv != 404) {
			t.Fatalf("round %d: delete=%d move=%d", i, del, mv)
		}
		orphans := e.count(`SELECT count(*) FROM folders c JOIN folders p ON c.parent_folder_id = p.id WHERE c.deleted_at IS NULL AND p.deleted_at IS NOT NULL`) +
			e.count(`SELECT count(*) FROM lists c JOIN folders p ON c.folder_id = p.id WHERE c.deleted_at IS NULL AND p.deleted_at IS NOT NULL`) +
			e.count(`SELECT count(*) FROM items c JOIN lists p ON c.list_id = p.id WHERE c.deleted_at IS NULL AND p.deleted_at IS NOT NULL`)
		if orphans != 0 {
			t.Fatalf("round %d (move=%d): %d active rows under a trashed parent", i, mv, orphans)
		}
	}
}

// ---- F012-B2: empty trash ----

func TestF012_EmptyTrash(t *testing.T) {
	e := newImageEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", f["id"])
	a := e.upload(e.titled(l["id"].(string), "A"), "a.png", pngFixture(t, 10, 10))
	b := e.upload(e.newItemInNewList("B"), "b.jpg", jpegFixture(t, 10, 10))
	if got := len(e.fileKeys()); got != 4 {
		t.Fatalf("files before = %d, want 4", got)
	}
	e.mustDel("folders", "f", f) // folder, its list and item A
	e.mustDel("items", "i", b)   // a second batch with its own image
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 2 {
		t.Fatalf("batches = %d, want 2", n)
	}

	wantStatus(t, e.call("DELETE", "/api/v1/trash", "", ""), 204)
	entries, page := e.trashPage("")
	if len(entries) != 0 || page["total"].(float64) != 0 {
		t.Fatalf("trash after empty = %v / %v", entries, page)
	}
	for q, want := range map[string]int{"folders": 0, "lists": 0, "items": 0, "trash_batches": 0, "attachments": 0} {
		if n := e.count("SELECT count(*) FROM " + q); n != want {
			t.Fatalf("%s: %d rows, want %d", q, n, want)
		}
	}
	if got := len(e.fileKeys()); got != 0 {
		t.Fatalf("files after = %v, want none", e.fileKeys())
	}
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, a)+"/file", "", ""), 404, "not_found")
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, b)+"/file", "", ""), 404, "not_found")
	e.wantConsistent()

	// Idempotent: a second call on the now empty trash is 204 too.
	wantStatus(t, e.call("DELETE", "/api/v1/trash", "", ""), 204)
	t.Run("fresh workspace", func(t *testing.T) {
		fresh := newImageEnv(t)
		wantStatus(t, fresh.call("DELETE", "/api/v1/trash", "", ""), 204)
	})
}

func TestF012_AutoPurge(t *testing.T) {
	e := newImageEnv(t)
	e.exec(`INSERT INTO workspace_settings (workspace_id) VALUES ($1)`, e.ws)
	svc := trash.New(e.pool, e.store, e.clk, quietLogger(), e.ws)

	old := e.upload(e.newItemInNewList("Old"), "old.png", pngFixture(t, 10, 10))
	e.mustDel("items", "i", old) // trashed 8 days before the run
	e.clk.Advance(2 * 24 * time.Hour)
	recent := e.upload(e.newItemInNewList("Recent"), "recent.png", pngFixture(t, 12, 12))
	e.mustDel("items", "i", recent) // trashed 6 days before the run
	e.clk.Advance(6 * 24 * time.Hour)
	if got := len(e.fileKeys()); got != 4 {
		t.Fatalf("files before = %d, want 4", got)
	}

	// Off (the default, NULL): nothing is purged however old the batches are.
	if n, err := svc.PurgeExpired(t.Context()); err != nil || n != 0 {
		t.Fatalf("off: PurgeExpired = %d, %v; want 0, nil", n, err)
	}
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 2 {
		t.Fatalf("off: batches = %d, want 2", n)
	}

	// Retention 7 days: the 8-day-old batch goes with its file, the 6-day-old stays.
	e.exec(`UPDATE workspace_settings SET trash_retention_days = 7 WHERE workspace_id = $1`, e.ws)
	n, err := svc.PurgeExpired(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("retention 7: PurgeExpired = %d, %v; want 1, nil", n, err)
	}
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 1 {
		t.Fatalf("batches = %d, want 1 (the 6-day-old one)", n)
	}
	if n := e.count(`SELECT count(*) FROM items WHERE id = $1`, old["id"]); n != 0 {
		t.Fatalf("old item rows = %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM items WHERE id = $1`, recent["id"]); n != 1 {
		t.Fatalf("recent item rows = %d, want 1", n)
	}
	if got := len(e.fileKeys()); got != 2 {
		t.Fatalf("files after = %v, want the recent item's two", e.fileKeys())
	}
	wantProblem(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, old)+"/file", "", ""), 404, "not_found")
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, recent)+"/file", "", ""), 200)
	e.wantConsistent()

	// A second run finds nothing left to purge.
	if n, err := svc.PurgeExpired(t.Context()); err != nil || n != 0 {
		t.Fatalf("second run: PurgeExpired = %d, %v; want 0, nil", n, err)
	}
}
