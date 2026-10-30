package httpapi_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/seed"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// newRecentEnv wires every service the recent-items tests touch: items, content saves, trash and search.
func newRecentEnv(t *testing.T) *treeEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	if err := seed.BuiltinTypes(t.Context(), sqlc.New(pool), ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspace_settings (workspace_id) VALUES ($1)", ws); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := newRouter(t, httpapi.Deps{
		DB: pool, DataDir: dir, Logger: quietLogger(), WorkspaceID: ws,
		Items:  newItems(pool, clock.System{}),
		Search: search.NewService(search.NewPostgres(pool)),
		Trash:  trash.NewService(trash.NewPostgres(pool), store, clock.System{}, quietLogger()),
	})
	return &treeEnv{t: t, h: h, pool: pool, ws: ws}
}

// recent returns GET /recent-items.
func (e *treeEnv) recent(query string) searchRows {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/recent-items"+query, "", "")
	wantStatus(e.t, rec, 200)
	var out searchRows
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestB12_RecentItemsOrderAndPath(t *testing.T) {
	e := newRecentEnv(t)
	media := e.folder("Media", nil)
	watch := e.listOfType("Watchlist", media["id"], "movie")
	wid := watch["id"].(string)

	a := e.titled(wid, "A")
	b := e.titled(wid, "B")
	e.titled(wid, "C")
	if got := e.recent("").titles(); !slices.Equal(got, []string{"C", "B", "A"}) {
		t.Fatalf("by creation: %v", got)
	}

	// A PATCH moves an item to the top.
	wantStatus(t, e.patchItem(a, `{"title":"A2"}`), 200)
	if got := e.recent("").titles(); !slices.Equal(got, []string{"A2", "C", "B"}) {
		t.Fatalf("after PATCH: %v", got)
	}

	// So does a content save (it bumps updated_at but not items.version).
	e.save("/api/v1/items/"+b["id"].(string)+"/content", 0, "# Notes")
	got := e.recent("")
	if !slices.Equal(got.titles(), []string{"B", "A2", "C"}) {
		t.Fatalf("after content save: %v", got.titles())
	}

	wantPath := []obj{
		{"type": "folder", "id": media["id"], "name": "Media", "icon": nil, "icon_color": nil},
		{"type": "list", "id": wid, "name": "Watchlist", "icon": nil, "icon_color": nil},
	}
	if !reflect.DeepEqual(got.Data[0].Path, wantPath) {
		t.Fatalf("path = %v, want %v", got.Data[0].Path, wantPath)
	}
	if m, ok := got.Data[0].Item["metadata"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("item summary metadata = %v, want {}", got.Data[0].Item["metadata"])
	}
	if got.Data[0].Item["type"] != "movie" {
		t.Errorf("TestF007 type = %v, want the list's type", got.Data[0].Item["type"])
	}
	if got.Data[0].Item["content_version"] != float64(1) {
		t.Errorf("content_version = %v", got.Data[0].Item["content_version"])
	}
	if _, ok := e.recentRaw()["page"]; ok {
		t.Error("recent items are not paginated: no page object")
	}
}

func (e *treeEnv) recentRaw() obj {
	e.t.Helper()
	rec := e.call("GET", "/api/v1/recent-items", "", "")
	wantStatus(e.t, rec, 200)
	return decode(e.t, rec)
}

func TestB12_RecentItemsExcludesTrash(t *testing.T) {
	e := newRecentEnv(t)
	keep := e.list("Keep", nil)
	gone := e.list("Gone", nil)
	e.titled(keep["id"].(string), "Visible")
	e.titled(gone["id"].(string), "In trashed list")
	loose := e.titled(keep["id"].(string), "Trashed item")

	wantStatus(t, e.call("DELETE", "/api/v1/items/"+loose["id"].(string), "", etagOf("i", loose)), 204)
	wantStatus(t, e.call("DELETE", "/api/v1/lists/"+gone["id"].(string), "", etagOf("l", gone)), 204)

	if got := e.recent("").titles(); !slices.Equal(got, []string{"Visible"}) {
		t.Fatalf("titles = %v, want only Visible", got)
	}
}

func TestB12_RecentItemsLimit(t *testing.T) {
	e := newTreeEnv(t)
	l := e.list("L", nil)["id"].(string)
	for i := 1; i <= 15; i++ {
		e.titled(l, fmt.Sprintf("Item %02d", i))
	}
	if n := len(e.recent("").Data); n != 12 {
		t.Fatalf("default limit: %d items, want 12", n)
	}
	got := e.recent("?limit=3").titles()
	if want := []string{"Item 15", "Item 14", "Item 13"}; !slices.Equal(got, want) {
		t.Fatalf("limit=3: %v, want %v", got, want)
	}
	if n := len(e.recent("?limit=50").Data); n != 15 {
		t.Fatalf("limit=50: %d items, want 15", n)
	}
	if n := len(e.recent("?limit=1").Data); n != 1 {
		t.Fatalf("limit=1: %d items", n)
	}
}

func TestB12_RecentItemsRejectsBadLimit(t *testing.T) {
	e := newTreeEnv(t)
	for _, limit := range []string{"0", "51", "-1", "abc", ""} {
		t.Run("limit="+limit, func(t *testing.T) {
			wantProblem(t, e.call("GET", "/api/v1/recent-items?limit="+limit, "", ""), 400, "invalid_parameter")
		})
	}
}

func TestB12_RecentItemsEmpty(t *testing.T) {
	e := newTreeEnv(t)
	rec := e.call("GET", "/api/v1/recent-items", "", "")
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got != `{"data":[]}`+"\n" && got != `{"data":[]}` {
		t.Fatalf("body = %q, want {\"data\":[]}", got)
	}
}
