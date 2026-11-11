package httpapi_test

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/folders"
	"github.com/0xfederama/collections-app-backend/internal/lists"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// Share tests (F-027 B3): user "a" owns, "b" and "c" are other people of the same workspace.

func (e *imageEnv) share(folder obj, email string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("POST", "/api/v1/folders/"+str(folder, "id")+"/members", fmt.Sprintf(`{"email":%q}`, email), "")
}

func (e *imageEnv) mustShare(folder obj, user string) obj {
	e.t.Helper()
	rec := e.share(folder, user+"@test.dev")
	wantStatus(e.t, rec, 200)
	return decode(e.t, rec)
}

func (e *imageEnv) unshare(folder obj, user string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("DELETE", "/api/v1/folders/"+str(folder, "id")+"/members/"+testUsers[user].String(), "", "")
}

func (e *imageEnv) move(typ, prefix string, o obj, field string, to any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call("POST", "/api/v1/"+typ+"/"+str(o, "id")+"/move", fmt.Sprintf(`{%q:%s}`, field, jsonOf(to)), etagOf(prefix, o))
}

func jsonOf(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%q", v)
}

func (e *imageEnv) moveFolder(f obj, to any) *httptest.ResponseRecorder {
	return e.move("folders", "f", f, "parent_folder_id", to)
}
func (e *imageEnv) moveList(l obj, to any) *httptest.ResponseRecorder {
	return e.move("lists", "l", l, "folder_id", to)
}

// fresh re-reads a folder or list as the env's user.
func (e *imageEnv) fresh(typ string, o obj) obj { return e.fetch(typ, str(o, "id")) }

func memberIDs(o obj) []string {
	var out []string
	for _, v := range o["member_user_ids"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func owner(o obj) string {
	if o["owner_user_id"] == nil {
		return ""
	}
	return str(o, "owner_user_id")
}

func has(rows []obj, o obj) bool {
	for _, r := range rows {
		if r["id"] == o["id"] {
			return true
		}
	}
	return false
}

func TestF027_Shares_ShareAndPayloads(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	nested := e.folder("Nested", fa["id"])
	la := e.list("In shared", fa["id"])
	b, c := e.as("b"), e.as("c")

	if fa["shared"] != false || len(memberIDs(fa)) != 0 {
		t.Fatalf("a new folder is unshared: %v", fa)
	}
	rec := e.share(fa, "  B@Test.DEV ")
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["shared"] != true || !slices.Equal(memberIDs(got), []string{testUsers["b"].String()}) {
		t.Fatalf("shared payload = %v", got)
	}
	if got["version"] != fa["version"] || rec.Header().Get("ETag") != etagOf("f", fa) {
		t.Errorf("sharing bumped the version: %v -> %v", fa["version"], got["version"])
	}
	// Again: 200, unchanged, one member.
	again := decode(t, mustStatus(t, e.share(fa, "b@test.dev"), 200))
	if len(memberIDs(again)) != 1 {
		t.Errorf("members after a second share = %v", memberIDs(again))
	}

	// B sees the folder at their top level with its content; A's nested folder stays unshared in the payload.
	if g := b.fresh("folders", fa); owner(g) != testUsers["a"].String() || g["shared"] != true {
		t.Errorf("b GET folder = %v", g)
	}
	if !has(b.tree("folders"), fa) || !has(b.tree("folders"), nested) || !has(b.tree("lists"), la) {
		t.Errorf("b tree misses the shared box: %v %v", names(b.tree("folders")), names(b.tree("lists")))
	}
	for _, f := range b.tree("folders") {
		if f["id"] == nested["id"] && (f["shared"] != false || len(memberIDs(f)) != 0) {
			t.Errorf("nested folder payload = %v", f)
		}
	}
	// C sees nothing.
	if has(c.tree("folders"), fa) {
		t.Error("c sees a folder not shared with them")
	}
	wantProblem(t, c.call("GET", "/api/v1/folders/"+str(fa, "id"), "", ""), 404, "not_found")
}

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	wantStatus(t, rec, status)
	return rec
}

func TestF027_Shares_ShareErrors(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	nested := e.folder("Nested", fa["id"])
	e.mustShare(fa, "b")
	b, c := e.as("b"), e.as("c")

	t.Run("a member is not the owner", func(t *testing.T) {
		wantProblem(t, b.share(fa, "c@test.dev"), 403, "forbidden")
	})
	t.Run("a nested folder cannot be shared", func(t *testing.T) {
		wantProblem(t, e.share(nested, "c@test.dev"), 409, "folder_not_top_level")
		// Also for the member: the nesting answer comes before the owner answer.
		wantProblem(t, b.share(nested, "c@test.dev"), 409, "folder_not_top_level")
	})
	t.Run("unknown and own email are 422 on email", func(t *testing.T) {
		for _, tc := range []struct{ email, msg string }{
			{"nobody@test.dev", "no user with this email has signed in yet"},
			{"A@test.dev", "you already own this folder"},
		} {
			p := wantProblem(t, e.share(fa, tc.email), 422, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "email" || p.Errors[0].Message != tc.msg {
				t.Errorf("%s: errors = %v", tc.email, p.Errors)
			}
		}
	})
	t.Run("a stranger and a trashed folder are 404", func(t *testing.T) {
		wantProblem(t, c.share(fa, "a@test.dev"), 404, "not_found")
		gone := e.folder("Gone", nil)
		e.mustDel("folders", "f", gone)
		wantProblem(t, e.share(gone, "b@test.dev"), 404, "not_found")
	})
}

func TestF027_Shares_RemoveAndLeave(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	e.mustShare(fa, "c")
	b, c := e.as("b"), e.as("c")

	// B gave the shared folder a place in their own order.
	wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q]}`, str(fa, "id")), ""), 204)
	if n := e.count(`SELECT count(*) FROM user_top_order WHERE user_id = $1 AND entry_id = $2`, testUsers["b"], str(fa, "id")); n != 1 {
		t.Fatalf("order rows of b = %d, want 1", n)
	}

	wantProblem(t, b.unshare(fa, "c"), 403, "forbidden") // a member removes only themself
	wantProblem(t, e.unshare(fa, "a"), 404, "not_found") // the owner is not a member
	wantProblem(t, e.as("b").call("DELETE", "/api/v1/folders/"+str(fa, "id")+"/members/"+uuid.NewString(), "", ""), 403, "forbidden")

	wantStatus(t, b.unshare(fa, "b"), 204) // leave
	if n := e.count(`SELECT count(*) FROM user_top_order WHERE user_id = $1 AND entry_id = $2`, testUsers["b"], str(fa, "id")); n != 0 {
		t.Errorf("order rows of b after leaving = %d, want 0", n)
	}
	wantProblem(t, b.call("GET", "/api/v1/folders/"+str(fa, "id"), "", ""), 404, "not_found")
	if got := e.fresh("folders", fa); !slices.Equal(memberIDs(got), []string{testUsers["c"].String()}) {
		t.Errorf("members after b left = %v", memberIDs(got))
	}
	wantProblem(t, e.unshare(fa, "b"), 404, "not_found") // not a member any more
	wantStatus(t, e.unshare(fa, "c"), 204)               // owner removes
	if got := e.fresh("folders", fa); got["shared"] != false {
		t.Errorf("folder still shared: %v", got)
	}
	wantProblem(t, c.call("GET", "/api/v1/folders/"+str(fa, "id"), "", ""), 404, "not_found")
}

func TestF027_Shares_MemberCanEditInsideButNotMoveOrDeleteTheRoot(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	own := e.folder("A private", nil)
	e.mustShare(fa, "b")
	b := e.as("b")
	fb := b.folder("B own", nil)

	// Top level: B cannot move or delete the shared folder, A's other entries stay invisible.
	wantProblem(t, b.moveFolder(fa, fb["id"]), 403, "forbidden")
	wantProblem(t, b.moveFolder(fa, nil), 403, "forbidden")
	wantProblem(t, b.del("folders", "f", fa), 403, "forbidden")
	wantProblem(t, b.moveFolder(own, fb["id"]), 404, "not_found")

	// Rename is allowed, and the owner sees it.
	rec := b.call("PATCH", "/api/v1/folders/"+str(fa, "id"), `{"name":"Renamed"}`, etagOf("f", fa))
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["name"] != "Renamed" || got["shared"] != true {
		t.Errorf("renamed = %v", got)
	}

	// Everything inside: create, edit, move inside, delete inside, reorder inside.
	sub := b.folder("Sub", fa["id"])
	sub2 := b.folder("Sub2", fa["id"])
	l := b.list("Inside", sub["id"])
	if owner(sub) != "" || owner(l) != "" {
		t.Errorf("nested rows carry an owner: %v %v", sub, l)
	}
	it := b.titled(str(l, "id"), "Made by b")
	wantStatus(t, b.patchItem(it, `{"title":"Edited by b"}`), 200)
	if got := e.fetchItem(it["id"]); got["title"] != "Edited by b" {
		t.Errorf("a sees %v", got["title"])
	}
	wantStatus(t, b.moveList(l, sub2["id"]), 200)
	wantStatus(t, b.moveFolder(sub2, sub["id"]), 200)
	wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":%q,"ids":[%q]}`, str(fa, "id"), str(sub, "id")), ""), 204)
	wantStatus(t, b.del("items", "i", b.fetchItem(it["id"])), 204)
	wantStatus(t, b.del("folders", "f", b.fresh("folders", sub2)), 204)

	// The owner still deletes and moves the root: moving it into A's own folder is blocked while shared.
	wantProblem(t, e.moveFolder(e.fresh("folders", fa), own["id"]), 409, "folder_shared")
	wantStatus(t, e.moveFolder(e.fresh("folders", fa), nil), 200)
	// Moving a private folder into the (now top-level again, still shared) folder is a move into a shared box.
	wantStatus(t, e.moveFolder(e.fresh("folders", own), fa["id"]), 200)
}

func TestF027_Shares_OwnOrderOfASharedFolder(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	b := e.as("b")
	fb := b.folder("B own", nil)

	// B reorders the shared folder among their own top level; A's order is untouched.
	wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q]}`, str(fb, "id"), str(fa, "id")), ""), 204)
	if got := names(b.tree("folders")); got[0] != "B own" || got[1] != "Shared" {
		t.Errorf("b order = %v", got)
	}
	wantStatus(t, b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q]}`, str(fa, "id"), str(fb, "id")), ""), 204)
	if got := names(b.tree("folders")); got[0] != "Shared" {
		t.Errorf("b order = %v", got)
	}
	if got := names(e.tree("folders")); len(got) != 1 {
		t.Errorf("a sees %v", got)
	}
}

func TestF027_Shares_MovesBetweenBoxes(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	b, c := e.as("b"), e.as("c")
	bid, aid := testUsers["b"].String(), testUsers["a"].String()

	t.Run("into a shared folder the other member sees it and nobody owns it", func(t *testing.T) {
		priv := e.list("Private list", nil)
		wantProblem(t, b.call("GET", "/api/v1/lists/"+str(priv, "id"), "", ""), 404, "not_found")
		moved := decode(t, mustStatus(t, e.moveList(priv, fa["id"]), 200))
		if owner(moved) != "" {
			t.Errorf("owner after the move = %q, want none", owner(moved))
		}
		if !has(b.tree("lists"), priv) {
			t.Error("b does not see the list moved into the shared folder")
		}
	})

	t.Run("a nested folder with a list and an item leaves by a member: the mover owns it", func(t *testing.T) {
		sub := e.folder("Sub", fa["id"])
		l := e.list("Sub list", sub["id"])
		it := e.titled(str(l, "id"), "Sub item")
		moved := decode(t, mustStatus(t, b.moveFolder(sub, nil), 200))
		if owner(moved) != bid {
			t.Errorf("owner after leaving = %q, want b", owner(moved))
		}
		// B owns the whole subtree now; A lost it.
		if !has(b.tree("folders"), sub) || !has(b.tree("lists"), l) {
			t.Error("b lost the folder they moved out")
		}
		wantStatus(t, b.call("GET", "/api/v1/items/"+str(it, "id"), "", ""), 200)
		for _, url := range []string{"/folders/" + str(sub, "id"), "/lists/" + str(l, "id"), "/items/" + str(it, "id")} {
			wantProblem(t, e.call("GET", "/api/v1"+url, "", ""), 404, "not_found")
		}
		// And moving it back into the shared folder makes it shared again.
		back := decode(t, mustStatus(t, b.moveFolder(b.fresh("folders", sub), fa["id"]), 200))
		if owner(back) != "" {
			t.Errorf("owner after moving back = %q", owner(back))
		}
		wantStatus(t, e.call("GET", "/api/v1/items/"+str(it, "id"), "", ""), 200)
	})

	t.Run("a list leaves to the top level of whoever moves it, owner included", func(t *testing.T) {
		l := b.list("B adds", fa["id"])
		moved := decode(t, mustStatus(t, b.moveList(l, nil), 200))
		if owner(moved) != bid {
			t.Errorf("owner = %q, want b", owner(moved))
		}
		wantProblem(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 404, "not_found")

		mine := e.list("A adds", fa["id"])
		moved = decode(t, mustStatus(t, e.moveList(mine, nil), 200))
		if owner(moved) != aid {
			t.Errorf("owner = %q, want a", owner(moved))
		}
		wantProblem(t, b.call("GET", "/api/v1/lists/"+str(mine, "id"), "", ""), 404, "not_found")
	})

	t.Run("out into one of the mover's private folders leaves no owner", func(t *testing.T) {
		l := e.list("Moves out", fa["id"])
		fb := b.folder("B private", nil)
		moved := decode(t, mustStatus(t, b.moveList(l, fb["id"]), 200))
		if owner(moved) != "" {
			t.Errorf("owner = %q, want none", owner(moved))
		}
		wantProblem(t, e.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 404, "not_found")
		wantStatus(t, b.call("GET", "/api/v1/lists/"+str(l, "id"), "", ""), 200)
	})

	t.Run("a destination the mover cannot see is unavailable", func(t *testing.T) {
		l := e.list("Stays", fa["id"])
		other := e.folder("A private", nil)
		wantProblem(t, b.moveList(l, other["id"]), 409, "destination_unavailable")
		wantProblem(t, c.moveList(l, nil), 404, "not_found")
	})
}

func TestF027_Shares_TrashOfSharedContent(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	b := e.as("b")
	priv := e.list("A private", nil)
	e.mustDel("lists", "l", priv)

	l1 := e.list("Deleted by a", fa["id"])
	l2 := b.list("Deleted by b", fa["id"])
	e.mustDel("lists", "l", l1)
	b.mustDel("lists", "l", l2)

	// Both see both batches in the owner's trash; B does not see A's private one.
	for name, env := range map[string]*imageEnv{"a": e, "b": b} {
		entries, _ := env.trashPage("")
		if !has(entries, obj{"id": env.entry(l1["id"])["id"]}) || !has(entries, obj{"id": env.entry(l2["id"])["id"]}) {
			t.Errorf("%s misses a shared batch", name)
		}
	}
	for _, en := range func() []obj { r, _ := b.trashPage(""); return r }() {
		if en["root_id"] == priv["id"] {
			t.Error("b sees A's private trash entry")
		}
	}
	// Each restores the other's.
	wantStatus(t, b.restore(b.entry(l1["id"]), ""), 200)
	wantStatus(t, e.restore(e.entry(l2["id"]), ""), 200)
	// Restore into the restorer's own top level: they own the list.
	b.mustDel("lists", "l", b.fresh("lists", l1))
	rec := b.restore(b.entry(l1["id"]), `{"destination":{"folder_id":null}}`)
	wantStatus(t, rec, 200)
	if got := b.fresh("lists", l1); owner(got) != testUsers["b"].String() {
		t.Errorf("owner after restoring to the top level = %q", owner(got))
	}
	// The other member purges a batch too.
	l3 := b.list("Purged by a", fa["id"])
	b.mustDel("lists", "l", l3)
	wantStatus(t, e.purge(e.entry(l3["id"])), 204)

	// Empty trash purges only what the caller sees.
	l4 := e.list("Gone by empty", fa["id"])
	e.mustDel("lists", "l", l4)
	wantStatus(t, b.call("DELETE", "/api/v1/trash", "", ""), 204)
	e.noEntry(l4["id"])
	e.entry(priv["id"]) // A's private batch survived B's empty

	// After the share stops, a batch from the shared folder is gone for the ex-member.
	l5 := e.list("Deleted before unshare", fa["id"])
	e.mustDel("lists", "l", l5)
	entry := e.entry(l5["id"])
	wantStatus(t, e.unshare(fa, "b"), 204)
	wantProblem(t, b.restore(entry, ""), 404, "not_found")
	wantProblem(t, b.purge(entry), 404, "not_found")
	if entries, _ := b.trashPage(""); len(entries) != 0 {
		t.Errorf("b still sees %d trash entries", len(entries))
	}
}

func TestF027_Shares_PurgeRemovesMemberRows(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	e.mustDel("folders", "f", e.fresh("folders", fa))
	// Trashed folders keep their members, and the members still see the batch.
	if n := e.count(`SELECT count(*) FROM folder_members WHERE folder_id = $1`, str(fa, "id")); n != 1 {
		t.Fatalf("members of the trashed folder = %d", n)
	}
	wantStatus(t, e.purge(e.entry(fa["id"])), 204)
	if n := e.count(`SELECT count(*) FROM folder_members WHERE folder_id = $1`, str(fa, "id")); n != 0 {
		t.Errorf("member rows after purge = %d, want 0", n)
	}
}

func TestF027_Shares_StoppingKeepsWhatTheMemberAdded(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	b := e.as("b")
	l := b.list("Added by b", fa["id"])
	it := b.titled(str(l, "id"), "Zebra added by b")
	if got := b.search(q("zebra")).titles(); !slices.Equal(got, []string{"Zebra added by b"}) {
		t.Fatalf("b search = %v", got)
	}
	if got := e.search(q("zebra")).titles(); !slices.Equal(got, []string{"Zebra added by b"}) {
		t.Fatalf("a search of the shared list = %v", got)
	}
	if got := b.recent("").titles(); !slices.Equal(got, []string{"Zebra added by b"}) {
		t.Fatalf("b recent = %v", got)
	}

	wantStatus(t, e.unshare(fa, "b"), 204)
	if got := b.search(q("zebra")).titles(); len(got) != 0 {
		t.Errorf("ex-member search = %v", got)
	}
	if got := b.recent("").titles(); len(got) != 0 {
		t.Errorf("ex-member recent = %v", got)
	}
	if len(b.tree("folders")) != 0 || len(b.tree("lists")) != 0 {
		t.Error("ex-member still has the box in the tree")
	}
	for _, url := range []string{"/folders/" + str(fa, "id"), "/lists/" + str(l, "id"), "/items/" + str(it, "id")} {
		wantProblem(t, b.call("GET", "/api/v1"+url, "", ""), 404, "not_found")
	}
	// Everything stays, in the owner's box.
	wantStatus(t, e.call("GET", "/api/v1/items/"+str(it, "id"), "", ""), 200)
	if !has(e.tree("lists"), l) {
		t.Error("the list the member added is gone for the owner")
	}
}

// The path guard and the destination check read the access snapshot before the transaction. These
// tests call the services without that pre-check and remove the share just before: only the
// re-read inside the transaction can stop the write.
func TestF027_Shares_MoveRacingAnUnshareIsStoppedInTheTransaction(t *testing.T) {
	e := newAccountsEnv(t)
	fa := e.folder("Shared", nil)
	e.mustShare(fa, "b")
	sub := e.folder("Sub", fa["id"])
	l := e.list("Inside", fa["id"])
	bctx := scope.WithUser(scope.With(t.Context(), e.ws), testUsers["b"])
	unshare := func() { e.exec(`DELETE FROM folder_members WHERE folder_id = $1`, str(fa, "id")) }

	t.Run("folder", func(t *testing.T) {
		svc := folders.NewService(folders.NewPostgres(e.pool), clock.System{})
		unshare()
		_, err := svc.Move(bctx, uuidOf(sub), 1, nil)
		wantNotFound(t, err)
		if got := e.fresh("folders", sub); owner(got) != "" || got["parent_folder_id"] != fa["id"] {
			t.Errorf("the folder left the box: %v", got)
		}
		e.mustShare(fa, "b")
	})
	t.Run("list", func(t *testing.T) {
		svc := lists.NewService(lists.NewPostgres(e.pool), noTypeSource{}, clock.System{})
		unshare()
		_, err := svc.Move(bctx, uuidOf(l), 1, nil)
		wantNotFound(t, err)
		if got := e.fresh("lists", l); owner(got) != "" || got["folder_id"] != fa["id"] {
			t.Errorf("the list left the box: %v", got)
		}
		e.mustShare(fa, "b")
	})
	t.Run("destination", func(t *testing.T) {
		// B owns a list and moves it into the shared folder just after being removed from it.
		bl := e.as("b").list("B list", nil)
		svc := lists.NewService(lists.NewPostgres(e.pool), noTypeSource{}, clock.System{})
		unshare()
		_, err := svc.Move(bctx, uuidOf(bl), 1, uuidPtr(fa))
		var un *apperr.ConflictError
		if !asConflict(err, &un) || un.Code != apperr.CodeDestinationUnavailable {
			t.Errorf("err = %v, want destination_unavailable", err)
		}
		e.mustShare(fa, "b")
	})
	t.Run("restore", func(t *testing.T) {
		e.mustDel("lists", "l", e.fresh("lists", l))
		entry := e.entry(l["id"])
		svc := trash.NewService(trash.NewPostgres(e.pool), nil, clock.System{}, quietLogger())
		unshare()
		_, err := svc.Restore(bctx, uuidOf(entry), &trash.Destination{FolderSet: true})
		wantNotFound(t, err)
		e.entry(l["id"]) // still in the trash
	})
	t.Run("share", func(t *testing.T) {
		// The owner stops being the owner of nothing: sharing a folder the caller cannot see is a 404.
		other := e.as("b").folder("B folder", nil)
		svc := folders.NewService(folders.NewPostgres(e.pool), clock.System{})
		_, err := svc.AddMember(scope.WithUser(scope.With(t.Context(), e.ws), testUsers["c"]), uuidOf(other), "a@test.dev")
		wantNotFound(t, err)
	})
}

func uuidOf(o obj) uuid.UUID { return uuid.MustParse(str(o, "id")) }

func uuidPtr(o obj) *uuid.UUID { id := uuidOf(o); return &id }

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	var nf *apperr.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %v, want not found", err)
	}
}

func asConflict(err error, target **apperr.ConflictError) bool { return errors.As(err, target) }
