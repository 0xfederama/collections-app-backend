package httpapi_test

import (
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Shared-folder rules (F-027 decisions 6 to 10) over the real identity middleware: A owns the
// top-level folder S with a full box inside, S is shared with B, C is nobody.

// sharedWorld is A's shared folder S with a box inside, plus a private box P of A.
type sharedWorld struct {
	e, b, c *imageEnv
	s       obj // S, top level, shared with B
	in      box // under S (its folders also list S itself, for the outsider matrix)
	private box // A's private box
}

func newSharedWorld(t *testing.T) sharedWorld {
	t.Helper()
	e := newAuthzEnv(t)
	w := sharedWorld{e: e, b: e.as("b"), c: e.as("c")}
	w.s = e.folder("S", nil)
	w.in = e.buildBox("S", w.s["id"])
	w.in.folders = append(w.in.folders, str(w.s, "id")) // S is a target too; [0] and [1] stay the inner ones
	w.in.ids = append(w.in.ids, str(w.s, "id"))
	w.private = e.buildBox("P", nil)
	e.mustShare(w.s, "b")
	return w
}

func (w sharedWorld) wantSeen(who string, env *imageEnv, bx box, want bool) {
	w.e.t.Helper()
	t := w.e.t
	rec := env.call("GET", "/api/v1/tree", "", "")
	wantStatus(t, rec, 200)
	for _, id := range bx.folders {
		if got := contains2(rec, id); got != want {
			t.Errorf("%s: folder %s in the tree = %v, want %v", who, id[:8], got, want)
		}
	}
	for _, id := range bx.lists {
		if got := contains2(rec, id); got != want {
			t.Errorf("%s: list %s in the tree = %v, want %v", who, id[:8], got, want)
		}
	}
}

func contains2(rec *httptest.ResponseRecorder, id string) bool {
	return strings.Contains(rec.Body.String(), id)
}

func TestF027B6_NonMemberIsLockedOutOfASharedFolderAndEverythingInside(t *testing.T) {
	w := newSharedWorld(t)
	runPathMatrix(t, w.c, w.e, w.in, "c")
	// B is a member of S only: A's private box stays invisible to them on every operation.
	runPathMatrix(t, w.b, w.e, w.private, "b")
}

func TestF027B6_MemberRightsInsideASharedFolder(t *testing.T) {
	w := newSharedWorld(t)
	e, b := w.e, w.b
	sid := str(w.s, "id")

	t.Run("reads everything inside", func(t *testing.T) {
		for _, c := range pathCases {
			if c.method != "GET" || c.kind == "trash" {
				continue
			}
			for _, id := range w.in.targets(c.kind) {
				if c.kind == "folder" && id == sid {
					continue
				}
				r := pathReplacer(id, nil)
				wantStatus(t, b.call("GET", r.Replace(c.path), "", ""), 200)
			}
		}
		wantStatus(t, b.call("GET", "/api/v1/folders/"+sid, "", ""), 200)
		w.wantSeen("b", b, w.in, true)
		w.wantSeen("b", b, w.private, false)
		if got := b.search(q("alpha")).titles(); len(got) != 2 || !slices.Contains(got, "S Alpha item") {
			t.Errorf("b search = %v, want S's two items only", got)
		}
		if got := b.recent("").titles(); len(got) == 0 {
			t.Error("b recent misses the shared items")
		}
		for _, title := range b.recent("").titles() {
			if title == "P Alpha item" || title == "P Alpha nested item" {
				t.Errorf("b recent contains A's private %q", title)
			}
		}
	})

	t.Run("edits, creates, moves within and deletes within", func(t *testing.T) {
		rec := b.call("PATCH", "/api/v1/folders/"+sid, `{"name":"S renamed"}`, etagOf("f", e.fetch("folders", sid)))
		wantStatus(t, rec, 200)
		if got := e.fetch("folders", sid); got["name"] != "S renamed" || got["owner_user_id"] != testUsers["a"].String() {
			t.Errorf("a sees %v", got)
		}
		item := e.fetch("items", w.in.items[0])
		wantStatus(t, b.patchItem(item, `{"title":"S edited by b"}`), 200)
		f := b.folder("B inside", sid)
		l := b.list("B list inside", f["id"])
		it := b.titled(str(l, "id"), "Created by b")
		wantStatus(t, e.call("GET", "/api/v1/items/"+str(it, "id"), "", ""), 200)
		wantStatus(t, b.moveList(l, sid), 200)
		wantStatus(t, b.moveFolder(f, w.in.folders[0]), 200)
		wantStatus(t, b.del("items", "i", b.fetchItem(it["id"])), 204)
		wantStatus(t, b.del("lists", "l", b.fresh("lists", l)), 204)
		wantStatus(t, b.del("folders", "f", b.fresh("folders", f)), 204)
		// The content, the image and the order are editable too.
		url := "/api/v1/items/" + w.in.items[0] + "/content"
		wantStatus(t, b.call("PUT", url, `{"body":"by b"}`, `"c-2"`), 200)
		wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":%q,"ids":[%q]}`, sid, w.in.folders[0]), ""), 204)
	})

	t.Run("sees, restores and purges the trash of S", func(t *testing.T) {
		entries, _ := b.trashPage("")
		var ids []string
		for _, en := range entries {
			ids = append(ids, str(en, "id"))
		}
		for _, id := range w.in.batches {
			if !slices.Contains(ids, id) {
				t.Errorf("b's trash misses batch %s of S", id[:8])
			}
		}
		for _, id := range w.private.batches {
			if slices.Contains(ids, id) {
				t.Errorf("b's trash lists A's private batch %s", id[:8])
			}
		}
		wantStatus(t, b.restore(obj{"id": w.in.batches[0]}, ""), 200)
		wantStatus(t, b.restore(obj{"id": w.in.batches[1]}, ""), 200)
		wantStatus(t, b.purge(obj{"id": w.in.batches[2]}), 204)
		wantProblem(t, e.purge(obj{"id": w.in.batches[2]}), 404, "not_found")
	})

	t.Run("owner-only operations on S are 403 for a member", func(t *testing.T) {
		fb := b.folder("B own", nil)
		cur := b.fresh("folders", w.s)
		wantProblem(t, b.del("folders", "f", cur), 403, "forbidden")
		wantProblem(t, b.moveFolder(cur, nil), 403, "forbidden")
		wantProblem(t, b.moveFolder(cur, fb["id"]), 403, "forbidden")
		wantProblem(t, b.share(cur, "c@test.dev"), 403, "forbidden")
		wantProblem(t, b.unshare(cur, "a"), 403, "forbidden") // removing the owner
		wantProblem(t, b.unshare(cur, "c"), 403, "forbidden") // removing someone else
		after := e.fetch("folders", sid)
		if after["shared"] != true || !slices.Equal(memberIDs(after), []string{testUsers["b"].String()}) || owner(after) != testUsers["a"].String() {
			t.Errorf("S changed after refused member operations: %v", after)
		}
		// C never got in.
		wantProblem(t, w.c.call("GET", "/api/v1/folders/"+sid, "", ""), 404, "not_found")
	})
}

func TestF027B6_StoppingAShareLocksTheFormerMemberOut(t *testing.T) {
	w := newSharedWorld(t)
	e, b := w.e, w.b
	sid := str(w.s, "id")
	wantStatus(t, b.call("GET", "/api/v1/folders/"+sid, "", ""), 200)
	if entries, _ := b.trashPage(""); len(entries) != len(w.in.batches) {
		t.Fatalf("b trash before = %d entries, want %d", len(entries), len(w.in.batches))
	}

	wantStatus(t, e.unshare(w.s, "b"), 204)
	runPathMatrix(t, b, e, w.in, "b")
	w.wantSeen("b", b, w.in, false)
	if entries, _ := b.trashPage(""); len(entries) != 0 {
		t.Errorf("b's trash still lists %d batches of S", len(entries))
	}
	if len(b.search(q("alpha")).Data) != 0 || len(b.recent("").Data) != 0 {
		t.Error("b still finds S's items")
	}
	wantStatus(t, b.call("DELETE", "/api/v1/trash", "", ""), 204)
	if entries, _ := e.trashPage(""); len(entries) != len(w.in.batches)+len(w.private.batches) {
		t.Errorf("b's empty-trash reached the batches of S: a has %d", len(entries))
	}
	// Re-adding the member restores access (the rows were never touched).
	e.mustShare(w.s, "b")
	wantStatus(t, b.call("GET", "/api/v1/lists/"+w.in.lists[1], "", ""), 200)
}

func TestF027B6_MovesIntoAndOutOfASharedFolder(t *testing.T) {
	w := newSharedWorld(t)
	e, b, c := w.e, w.b, w.c
	sid := str(w.s, "id")
	aid, bid := testUsers["a"].String(), testUsers["b"].String()

	t.Run("a private list moved into S is seen by the owner, and back out it is the mover's", func(t *testing.T) {
		l := b.list("B moves in", nil)
		item := b.titled(str(l, "id"), "B item moves")
		wantProblem(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 404, "not_found")
		moved := decode(t, mustStatus(t, b.moveList(l, sid), 200))
		if owner(moved) != "" {
			t.Errorf("owner inside S = %q", owner(moved))
		}
		wantStatus(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 200)
		wantStatus(t, e.call("GET", "/api/v1/items/"+str(item, "id"), "", ""), 200)
		wantProblem(t, c.call("GET", "/api/v1/items/"+str(item, "id"), "", ""), 404, "not_found")

		out := decode(t, mustStatus(t, b.moveList(b.fresh("lists", l), nil), 200))
		if owner(out) != bid {
			t.Errorf("owner after leaving S = %q, want b", owner(out))
		}
		wantProblem(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 404, "not_found")
		wantProblem(t, e.call("GET", "/api/v1/items/"+str(item, "id"), "", ""), 404, "not_found")
		wantStatus(t, b.call("GET", "/api/v1/items/"+str(item, "id"), "", ""), 200)
	})

	t.Run("a nested folder moved out of S into A's private folder is gone for B, all of it", func(t *testing.T) {
		pf := e.folder("A private target", nil)
		f := e.fetch("folders", w.in.folders[0]) // F with N, L2 and I2 inside
		wantStatus(t, b.call("GET", "/api/v1/items/"+w.in.items[1], "", ""), 200)
		moved := decode(t, mustStatus(t, e.moveFolder(f, pf["id"]), 200))
		if owner(moved) != "" {
			t.Errorf("owner of a nested folder = %q", owner(moved))
		}
		for _, id := range []string{w.in.folders[0], w.in.folders[1]} {
			wantProblem(t, b.call("GET", "/api/v1/folders/"+id, "", ""), 404, "not_found")
		}
		for _, url := range []string{
			"/api/v1/lists/" + w.in.lists[1], "/api/v1/lists/" + w.in.lists[1] + "/items", "/api/v1/items/" + w.in.items[1],
			"/api/v1/items/" + w.in.items[1] + "/content", "/api/v1/items/" + w.in.items[1] + "/content/versions",
		} {
			wantProblem(t, b.call("GET", url, "", ""), 404, "not_found")
		}
		wantStatus(t, e.call("GET", "/api/v1/items/"+w.in.items[1], "", ""), 200)
		// What stayed in S is still B's to read.
		wantStatus(t, b.call("GET", "/api/v1/lists/"+w.in.lists[0], "", ""), 200)
	})

	t.Run("a shared folder cannot go into another folder", func(t *testing.T) {
		pf := e.folder("A other target", nil)
		wantProblem(t, e.moveFolder(e.fresh("folders", w.s), pf["id"]), 409, "folder_shared")
		if got := e.fresh("folders", w.s); got["parent_folder_id"] != nil || owner(got) != aid || got["shared"] != true {
			t.Errorf("S after the refused move: %v", got)
		}
	})

	t.Run("restore with a destination follows the same rules", func(t *testing.T) {
		pf := e.folder("A restore target", nil)
		// B restores a batch of S into B's own top level: B owns it, A and C lose it.
		l := e.list("restore to b", sid)
		e.mustDel("lists", "l", l)
		entry := b.entry(l["id"])
		wantProblem(t, b.restore(entry, fmt.Sprintf(`{"destination":{"folder_id":%q}}`, str(pf, "id"))), 409, "destination_unavailable")
		wantStatus(t, b.restore(entry, `{"destination":{"folder_id":null}}`), 200)
		if got := b.fresh("lists", l); owner(got) != bid {
			t.Errorf("owner after restoring to b's top level = %q", owner(got))
		}
		wantProblem(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 404, "not_found")

		// A restores a batch of S into A's private folder: the list leaves the share.
		l2 := b.list("restore to a private", sid)
		b.mustDel("lists", "l", l2)
		wantStatus(t, e.restore(e.entry(l2["id"]), fmt.Sprintf(`{"destination":{"folder_id":%q}}`, str(pf, "id"))), 200)
		wantProblem(t, b.call("GET", "/api/v1/lists/"+str(l2, "id"), "", ""), 404, "not_found")
		wantStatus(t, e.call("GET", "/api/v1/lists/"+str(l2, "id"), "", ""), 200)

		// Without a destination it returns where it was: inside S, seen by both.
		l3 := e.list("restore in place", sid)
		e.mustDel("lists", "l", l3)
		wantStatus(t, b.restore(b.entry(l3["id"]), ""), 200)
		wantStatus(t, e.call("GET", "/api/v1/lists/"+str(l3, "id"), "", ""), 200)
		wantStatus(t, b.call("GET", "/api/v1/lists/"+str(l3, "id"), "", ""), 200)
		// C can neither restore nor purge any of it.
		l4 := e.list("c cannot", sid)
		e.mustDel("lists", "l", l4)
		en := e.entry(l4["id"])
		wantProblem(t, c.restore(en, ""), 404, "not_found")
		wantProblem(t, c.purge(en), 404, "not_found")
	})
}
