package httpapi_test

import (
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// These tests pin what every hard delete removes today (F019-B2 T3), so the
// explicit purge of T4 and the type and field delete of T5 can be checked
// against the same behaviour.

// newPurgeEnv is an imageEnv whose router also serves content, with a settings
// row (content needs it).
func newPurgeEnv(t *testing.T) *imageEnv {
	t.Helper()
	e := newImageEnv(t)
	if _, err := e.pool.Exec(t.Context(), "INSERT INTO workspace_settings (workspace_id) VALUES ($1)", e.ws); err != nil {
		t.Fatal(err)
	}
	e.h = newRouter(t, httpapi.Deps{
		DB: e.pool, DataDir: e.dir, Logger: quietLogger(), WorkspaceID: e.ws,
		Items: newItems(e.pool, e.clk), Images: e.svc,
		Search: search.NewService(search.NewPostgres(e.pool)), Trash: trash.NewService(trash.NewPostgres(e.pool), e.store, e.clk, quietLogger()),
	})
	return e
}

// purgeTree is a folder > sub folder > book list with two items: one with two
// content versions and a lookup import, one with an image.
type purgeTree struct {
	folder, sub, list, withContent, withImage obj
}

func (e *imageEnv) buildPurgeTree(name string) purgeTree {
	e.t.Helper()
	f := e.folder(name, nil)
	sub := e.folder("Sub", f["id"])
	l := e.listOfType("Books", sub["id"], "book")
	lid := l["id"].(string)

	withContent := e.titled(lid, name+" notes")
	url := "/api/v1/items/" + withContent["id"].(string) + "/content"
	e.save(url, 0, "first")
	e.save(url, 1, "second")
	e.exec(`INSERT INTO lookup_imports (workspace_id, item_id, type, original_title, fetched_at, provider, source_id)
		VALUES ($1, $2, 'book', $3, now(), 'openlibrary', 'OL27448W')`, e.ws, withContent["id"], name)

	withImage := e.upload(e.titled(lid, name+" cover"), "cover.png", pngFixture(e.t, 10, 10))
	return purgeTree{folder: f, sub: sub, list: l, withContent: withContent, withImage: withImage}
}

// wantNoPurgeLeftovers fails if any row of the purged tree or any image file is left.
func (e *imageEnv) wantNoPurgeLeftovers() {
	e.t.Helper()
	for _, table := range []string{"folders", "lists", "items", "lookup_imports", "attachments", "trash_batches"} {
		if n := e.count("SELECT count(*) FROM "+table+" WHERE workspace_id = $1", e.ws); n != 0 {
			e.t.Fatalf("%s: %d rows left, want 0", table, n)
		}
	}
	// item_content_versions has no workspace_id; each test has its own database.
	if n := e.count("SELECT count(*) FROM item_content_versions"); n != 0 {
		e.t.Fatalf("item_content_versions: %d rows left, want 0", n)
	}
	e.wantNoFiles()
}

func TestB2_PurgeLeavesNoOrphans(t *testing.T) {
	t.Run("purge batch", func(t *testing.T) {
		e := newPurgeEnv(t)
		pt := e.buildPurgeTree("Dune")
		e.mustDel("folders", "f", pt.folder)
		wantStatus(t, e.purge(e.entry(pt.folder["id"])), 204)
		e.wantNoPurgeLeftovers()
	})

	t.Run("purge all", func(t *testing.T) {
		e := newPurgeEnv(t)
		pt := e.buildPurgeTree("Dune")
		e.mustDel("folders", "f", pt.folder)
		wantStatus(t, e.call("DELETE", "/api/v1/trash", "", ""), 204)
		e.wantNoPurgeLeftovers()
	})

	t.Run("expired auto purge", func(t *testing.T) {
		e := newPurgeEnv(t)
		pt := e.buildPurgeTree("Dune")
		e.mustDel("folders", "f", pt.folder)
		e.clk.Advance(2 * 24 * time.Hour)
		e.exec(`UPDATE workspace_settings SET trash_retention_days = 1 WHERE workspace_id = $1`, e.ws)
		svc := trash.NewService(trash.NewPostgres(e.pool), e.store, e.clk, quietLogger())
		if n, err := svc.PurgeExpired(scope.With(t.Context(), e.ws)); err != nil || n != 1 {
			t.Fatalf("PurgeExpired = %d, %v; want 1, nil", n, err)
		}
		e.wantNoPurgeLeftovers()
	})
}

func TestB2_PurgeKeepsOtherTrees(t *testing.T) {
	e := newPurgeEnv(t)
	a := e.buildPurgeTree("Alpha")
	b := e.buildPurgeTree("Beta")
	e.mustDel("folders", "f", a.folder)
	e.mustDel("folders", "f", b.folder)
	if n := e.count(`SELECT count(*) FROM trash_batches WHERE workspace_id = $1`, e.ws); n != 2 {
		t.Fatalf("batches = %d, want 2", n)
	}

	wantStatus(t, e.purge(e.entry(a.folder["id"])), 204)
	e.noEntry(a.folder["id"])

	// Alpha is gone; every row of Beta is still there.
	for q, want := range map[string]int{"folders": 2, "lists": 1, "items": 2, "lookup_imports": 1, "attachments": 1, "trash_batches": 1} {
		if n := e.count("SELECT count(*) FROM "+q+" WHERE workspace_id = $1", e.ws); n != want {
			t.Fatalf("%s: %d rows, want %d", q, n, want)
		}
	}
	if n := e.count(`SELECT count(*) FROM item_content_versions WHERE item_id = $1`, b.withContent["id"]); n != 2 {
		t.Fatalf("beta content versions = %d, want 2", n)
	}
	for _, it := range []obj{b.withContent, b.withImage} {
		if n := e.count(`SELECT count(*) FROM items WHERE id = $1`, it["id"]); n != 1 {
			t.Fatalf("beta item %v rows = %d, want 1", it["id"], n)
		}
	}
	if got := len(e.fileKeys()); got != 2 {
		t.Fatalf("files = %v, want Beta's two", e.fileKeys())
	}
	e.wantConsistent()
	wantStatus(t, e.call("GET", "/api/v1/attachments/"+attachmentIDOf(t, b.withImage)+"/file", "", ""), 200)

	// Beta comes back whole: its content and image survived the other purge.
	wantStatus(t, e.restore(e.entry(b.folder["id"]), ""), 200)
	if got := decode(t, e.call("GET", "/api/v1/items/"+b.withContent["id"].(string)+"/content", "", "")); got["body"] != "second" || got["version"] != float64(2) {
		t.Fatalf("beta content = %v", got)
	}
	e.fetchItem(b.withImage["id"])
}
