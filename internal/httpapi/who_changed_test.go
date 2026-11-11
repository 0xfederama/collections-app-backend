package httpapi_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Who-changed tests (F-027 B4): users "a" and "b" share one folder and edit inside it.

func wantBy(t *testing.T, what string, o obj, created, updated string) {
	t.Helper()
	want := func(user string) any {
		if user == "" {
			return nil
		}
		return testUsers[user].String()
	}
	if o["created_by_user_id"] != want(created) || o["updated_by_user_id"] != want(updated) {
		t.Errorf("%s: created_by=%v updated_by=%v, want %q/%q", what, o["created_by_user_id"], o["updated_by_user_id"], created, updated)
	}
}

func TestF027_WhoChanged_FolderAndListAttribution(t *testing.T) {
	e := newAccountsEnv(t)
	b := e.as("b")
	box := e.folder("Box", nil)
	wantBy(t, "created folder", box, "a", "a")
	e.mustShare(box, "b")

	sub := b.folder("Sub", box["id"])
	wantBy(t, "nested folder by b", sub, "b", "b")
	l := b.list("List", sub["id"])
	wantBy(t, "list by b", l, "b", "b")

	// Rename and list PATCH by the other user set updated_by only.
	rec := e.call("PATCH", "/api/v1/folders/"+str(sub, "id"), `{"name":"Sub2"}`, etagOf("f", sub))
	wantStatus(t, rec, 200)
	wantBy(t, "renamed by a", decode(t, rec), "b", "a")
	rec = e.call("PATCH", "/api/v1/lists/"+str(l, "id"), `{"name":"List2"}`, etagOf("l", l))
	wantStatus(t, rec, 200)
	wantBy(t, "list patched by a", decode(t, rec), "b", "a")

	// Move by b.
	other := b.folder("Other", box["id"])
	moved := decode(t, mustStatus(t, b.moveList(e.fresh("lists", l), other["id"]), 200))
	wantBy(t, "list moved by b", moved, "b", "b")

	// Reorder does not touch updated_by.
	b.call("PUT", "/api/v1/folders/order", fmt.Sprintf(`{"parent_folder_id":%q,"ids":[%q,%q]}`, str(box, "id"), str(other, "id"), str(sub, "id")), "")
	wantBy(t, "folder after reorder", e.fresh("folders", sub), "b", "a")

	// The tree carries the fields too.
	for _, f := range e.tree("folders") {
		if f["id"] == sub["id"] {
			wantBy(t, "tree folder", f, "b", "a")
		}
	}
}

func TestF027_WhoChanged_ItemAttribution(t *testing.T) {
	e := newAccountsEnv(t)
	b := e.as("b")
	box := e.folder("Box", nil)
	e.mustShare(box, "b")
	l1 := e.list("L1", box["id"])
	l2 := e.list("L2", box["id"])

	it := b.titled(str(l1, "id"), "Made by b")
	wantBy(t, "created item", it, "b", "b")

	rec := e.patchItem(it, `{"title":"Edited by a"}`)
	wantStatus(t, rec, 200)
	it = decode(t, rec)
	wantBy(t, "patched item", it, "b", "a")

	rec = b.call("POST", "/api/v1/items/"+str(it, "id")+"/move", fmt.Sprintf(`{"list_id":%q}`, str(l2, "id")), etagOf("i", it))
	wantStatus(t, rec, 200)
	it = decode(t, rec)
	wantBy(t, "moved item", it, "b", "b")

	// Image set and remove.
	it = e.setImageURLOK(it, "https://example.com/a.png")
	wantBy(t, "image set", it, "b", "a")
	rec = b.deleteImage(it)
	wantStatus(t, rec, 200)
	wantBy(t, "image removed", decode(t, rec), "b", "b")

	// List rows and the single read agree.
	rows, _ := e.page(str(l2, "id"), "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	wantBy(t, "list row", rows[0], "b", "b")
	wantBy(t, "get item", e.fetchItem(it["id"]), "b", "b")
}

func (e *imageEnv) setImageURLOK(it obj, url string) obj {
	e.t.Helper()
	rec := e.setURL(it, url)
	wantStatus(e.t, rec, 200)
	return decode(e.t, rec)
}

func TestF027_WhoChanged_ContentAuthorAndItemUpdatedBy(t *testing.T) {
	e := newAccountsEnv(t)
	b := e.as("b")
	box := e.folder("Box", nil)
	e.mustShare(box, "b")
	l := e.list("L", box["id"])
	it := e.titled(str(l, "id"), "Item")
	url := "/api/v1/items/" + str(it, "id") + "/content"
	e.exec("INSERT INTO workspace_settings (workspace_id) VALUES ($1) ON CONFLICT DO NOTHING", e.ws)

	if c := decode(t, mustStatus(t, e.call("GET", url, "", ""), 200)); c["author_user_id"] != nil {
		t.Errorf("version 0 author = %v", c["author_user_id"])
	}
	c1 := e.save(url, 0, "one")
	if c1["author_user_id"] != testUsers["a"].String() {
		t.Errorf("save by a: author = %v", c1["author_user_id"])
	}
	c2 := b.save(url, 1, "two")
	if c2["author_user_id"] != testUsers["b"].String() {
		t.Errorf("save by b: author = %v", c2["author_user_id"])
	}
	wantBy(t, "item after b's save", e.fetchItem(it["id"]), "a", "b")

	// Restore of version 1 by a creates version 3 authored by a.
	rec := e.call("POST", url+"/restore", `{"version":1}`, `"c-2"`)
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["author_user_id"] != testUsers["a"].String() || got["version"] != float64(3) {
		t.Errorf("restore = %v", got)
	}
	wantBy(t, "item after a's restore", e.fetchItem(it["id"]), "a", "a")

	// Old versions keep their author; the list and the version read show it.
	v := decode(t, mustStatus(t, e.call("GET", url+"/versions/2", "", ""), 200))
	if v["author_user_id"] != testUsers["b"].String() {
		t.Errorf("version 2 = %v", v)
	}
	list := decode(t, mustStatus(t, e.call("GET", url+"/versions", "", ""), 200))
	var got []any
	for _, row := range list["data"].([]any) {
		got = append(got, row.(map[string]any)["author_user_id"])
	}
	want := []any{testUsers["a"].String(), testUsers["b"].String(), testUsers["a"].String()}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("version authors = %v, want %v", got, want)
	}

	// A version saved before authorship was recorded has no author.
	e.exec(`UPDATE item_content_versions SET author_user_id = NULL WHERE item_id = $1 AND version = 1`, uuid.MustParse(str(it, "id")))
	if v := decode(t, mustStatus(t, e.call("GET", url+"/versions/1", "", ""), 200)); v["author_user_id"] != nil {
		t.Errorf("unknown author = %v", v["author_user_id"])
	}
}

func TestF027_WhoChanged_RestoreOfTheRootSetsUpdatedBy(t *testing.T) {
	e := newAccountsEnv(t)
	b := e.as("b")
	box := e.folder("Box", nil)
	e.mustShare(box, "b")
	l := e.list("L", box["id"])
	it := b.titled(str(l, "id"), "Item")
	b.mustDel("items", "i", it)
	wantStatus(t, e.restore(e.entry(it["id"]), ""), 200)
	wantBy(t, "restored item", e.fetchItem(it["id"]), "b", "a")

	e.mustDel("lists", "l", l)
	wantStatus(t, e.restore(e.entry(l["id"]), ""), 200)
	wantBy(t, "restored list", e.fresh("lists", l), "a", "a")
	sub := b.folder("Sub", box["id"])
	b.mustDel("folders", "f", sub)
	wantStatus(t, e.restore(e.entry(sub["id"]), ""), 200)
	wantBy(t, "restored folder", e.fresh("folders", sub), "b", "a")
}

func TestF027_WhoChanged_SystemWritesKeepUpdatedBy(t *testing.T) {
	e := newAccountsEnv(t)
	l := e.list("L", nil)
	it := e.titled(str(l, "id"), "Item")
	id := uuid.MustParse(str(it, "id"))
	q := sqlc.New(e.pool)

	// A refresh with no user in the context (the weekly job) passes a NULL caller.
	if err := q.UpdateLookupItem(scope.With(t.Context(), e.ws), sqlc.UpdateLookupItemParams{
		Title: "Refreshed", Metadata: []byte("{}"), Now: time.Now(), ID: id, WorkspaceID: e.ws,
	}); err != nil {
		t.Fatal(err)
	}
	got := e.fetchItem(it["id"])
	if got["title"] != "Refreshed" || got["version"] == it["version"] {
		t.Fatalf("refresh did not apply: %v", got)
	}
	wantBy(t, "item after system refresh", got, "a", "a")

	// A refresh with a user records them.
	if err := q.UpdateLookupItem(t.Context(), sqlc.UpdateLookupItemParams{
		Title: "Again", Metadata: []byte("{}"), Now: time.Now(), ID: id, WorkspaceID: e.ws,
		CallerUserID: uuid.NullUUID{UUID: testUsers["b"], Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	wantBy(t, "item after user refresh", e.fetchItem(it["id"]), "a", "b")
}

func TestF027_Users_ListIsOrderedByEmailAndNeedsOnlyAnIdentity(t *testing.T) {
	e := newAccountsEnv(t)
	for _, user := range []string{"a", "b", "c"} {
		rec := e.as(user).call("GET", "/api/v1/users", "", "")
		wantStatus(t, rec, 200)
		var emails []string
		for _, row := range decode(t, rec)["data"].([]any) {
			r := row.(map[string]any)
			emails = append(emails, r["email"].(string))
			if r["id"] == nil || r["name"] != nil || r["avatar_url"] != nil {
				t.Errorf("row = %v", r)
			}
		}
		if fmt.Sprint(emails) != "[a@test.dev b@test.dev c@test.dev]" {
			t.Errorf("%s sees %v", user, emails)
		}
	}
}
