package items_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

var (
	now   = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	later = now.Add(time.Hour)
)

// env is a migrated database with one bootstrapped workspace. ctx is scoped to it.
type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *items.Postgres
	ctx  context.Context
	ws   uuid.UUID
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return env{t: t, pool: pool, repo: items.NewPostgres(pool), ctx: scope.With(t.Context(), ws), ws: ws}
}

func ptr[T any](v T) *T { return &v }

// tx runs fn in one transaction through the repository.
func (e env) tx(fn func(r items.Repo) error) {
	e.t.Helper()
	if err := e.repo.InTx(e.ctx, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e env) insertFolder(parent *uuid.UUID, name string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO folders (workspace_id, parent_folder_id, name, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, parent, name).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// insertList inserts an active list of the given type (nil = no type) directly, as the list repository would.
func (e env) insertList(folder *uuid.UUID, typ *string, name string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO lists (workspace_id, folder_id, name, type, sort_order) VALUES ($1, $2, $3, $4, 0) RETURNING id`,
		e.ws, folder, name, typ).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e env) insert(it items.Item) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	e.tx(func(r items.Repo) error {
		var err error
		id, err = r.Insert(e.ctx, it, now)
		return err
	})
	return id
}

func (e env) get(id uuid.UUID) items.Item {
	e.t.Helper()
	var it items.Item
	e.tx(func(r items.Repo) error {
		var err error
		it, err = r.Get(e.ctx, id)
		return err
	})
	return it
}

// trash marks an item, list or folder as trashed under its own batch (the constraints need only this).
func (e env) trash(table string, id uuid.UUID) {
	e.t.Helper()
	var batch uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, $2, $3, 'x', '[]') RETURNING id`,
		e.ws, singular(table), id).Scan(&batch)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, fmt.Sprintf(`UPDATE %s SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, table), id, batch); err != nil {
		e.t.Fatal(err)
	}
}

func singular(table string) string {
	switch table {
	case "folders":
		return "folder"
	case "lists":
		return "list"
	}
	return "item"
}

// attachment inserts an uploaded image of the workspace and returns its id.
func (e env) attachment(width, height int, filename *string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO attachments (workspace_id, storage_key, thumbnail_storage_key, original_filename, media_type, size_bytes, width, height, sha256)
		VALUES ($1, $2, $3, $4, 'image/jpeg', 10, $5, $6, $7) RETURNING id`,
		e.ws, "images/aa/bb/"+uuid.NewString()+".jpg", "images/aa/bb/"+uuid.NewString()+".thumb.jpg", filename, width, height, make([]byte, 32)).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e env) setImageAttachment(item, attachment uuid.UUID) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET image_attachment_id = $2 WHERE id = $1`, item, attachment); err != nil {
		e.t.Fatal(err)
	}
}

func (e env) addContentVersion(item uuid.UUID, version int) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, $2, 'body', 4, $3)`,
		item, version, make([]byte, 32)); err != nil {
		e.t.Fatal(err)
	}
}

// sortRows inserts an item at a sort order, directly, so the test controls the order.
func (e env) sortRow(list uuid.UUID, title string, sortOrder int) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO items (workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, $4) RETURNING id`,
		e.ws, list, title, sortOrder).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}

func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestRepoInsertGetRoundTrip(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, ptr("vehicle"), "Garage")
	site := "https://example.com/bike"
	meta := json.RawMessage(`{"make":"Ducati","model_year":2020,"tags":["a","b"],"nested":{"x":1.5}}`)
	id := e.insert(items.Item{
		ListID: list, Title: "Panigale", SortOrder: 3, Website: &site, Metadata: meta,
		Price: &items.Price{Min: ptr("1299.00"), Max: ptr("1499.00"), Currency: "EUR"},
	})

	got := e.get(id)
	if got.ID != id || got.ListID != list || got.Title != "Panigale" || got.SortOrder != 3 || got.Version != 1 {
		t.Fatalf("stored columns: %+v", got)
	}
	if got.Type == nil || *got.Type != "vehicle" {
		t.Errorf("type = %v, want the list's vehicle", got.Type)
	}
	if got.Website == nil || *got.Website != site {
		t.Errorf("website = %v", got.Website)
	}
	if got.Price == nil || *got.Price.Min != "1299.00" || *got.Price.Max != "1499.00" || got.Price.Currency != "EUR" || got.Price.Amount != nil {
		t.Errorf("price = %+v", got.Price)
	}
	if !sameJSON(t, got.Metadata, meta) {
		t.Errorf("metadata = %s, want %s", got.Metadata, meta)
	}
	if got.ContentVersion != 0 || got.Path != nil || got.Image != nil {
		t.Errorf("content version, path and image must stay unset by Get: %+v", got)
	}
}

func TestRepoPriceRoundTripExactDecimals(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, ptr("wishlist"), "Wish")
	cases := []struct {
		name  string
		price *items.Price
		want  *items.Price
	}{
		{"nil price stays nil", nil, nil},
		{"min and max with leading zero", &items.Price{Min: ptr("0.05"), Max: ptr("10.10"), Currency: "USD"},
			&items.Price{Min: ptr("0.05"), Max: ptr("10.10"), Currency: "USD"}},
		{"min only", &items.Price{Min: ptr("5.00"), Currency: "GBP"}, &items.Price{Min: ptr("5.00"), Currency: "GBP"}},
		{"max only", &items.Price{Max: ptr("99999999.99"), Currency: "EUR"}, &items.Price{Max: ptr("99999999.99"), Currency: "EUR"}},
		{"single amount is min and max", &items.Price{Amount: ptr("12.00"), Currency: "EUR"},
			&items.Price{Min: ptr("12.00"), Max: ptr("12.00"), Currency: "EUR"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := e.insert(items.Item{ListID: list, Title: "P", Price: c.price, Metadata: json.RawMessage(`{}`)})
			got := e.get(id).Price
			if (got == nil) != (c.want == nil) {
				t.Fatalf("price = %+v, want %+v", got, c.want)
			}
			if got == nil {
				return
			}
			if !equalPtrs(got.Min, c.want.Min) || !equalPtrs(got.Max, c.want.Max) || got.Currency != c.want.Currency {
				t.Errorf("price = %+v, want %+v", got, c.want)
			}
		})
	}
}

func equalPtrs(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestRepoInsertNilMetadataIsEmptyObject(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Untyped")
	id := e.insert(items.Item{ListID: list, Title: "No type"})
	got := e.get(id)
	if string(got.Metadata) != "{}" {
		t.Errorf("metadata = %s, want {}", got.Metadata)
	}
	if got.Type != nil {
		t.Errorf("type = %v, want nil for a list with no type", *got.Type)
	}
}

func TestRepoGetNotFoundForMissingAndTrashed(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "L")
	trashed := e.sortRow(list, "gone", 0)
	e.trash("items", trashed)
	for name, id := range map[string]uuid.UUID{"missing": uuid.New(), "trashed": trashed} {
		t.Run(name, func(t *testing.T) {
			var err error
			e.tx(func(r items.Repo) error {
				_, err = r.Get(e.ctx, id)
				return nil
			})
			if !isNotFound(err) {
				t.Errorf("err = %v, want NotFound", err)
			}
		})
	}
}

func TestRepoAttachmentDimensions(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Pics")
	id := e.insert(items.Item{ListID: list, Title: "Photo", Metadata: json.RawMessage(`{}`)})
	att := e.attachment(800, 600, ptr("pic.jpg"))
	e.setImageAttachment(id, att)

	img := e.get(id).Image
	if img == nil {
		t.Fatal("image is nil")
	}
	want := items.Image{
		Source: items.ImageSourceUpload, URL: "/api/v1/attachments/" + att.String() + "/file",
		AttachmentID: att, ThumbnailURL: "/api/v1/attachments/" + att.String() + "/thumbnail",
		Width: 800, Height: 600, OriginalFilename: ptr("pic.jpg"),
	}
	if !reflect.DeepEqual(*img, want) {
		t.Errorf("image = %+v, want %+v", *img, want)
	}

	// An external URL image has no dimensions.
	url := "https://example.com/cover.png"
	other := e.insert(items.Item{ListID: list, Title: "Linked", Metadata: json.RawMessage(`{}`)})
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET image_url = $2 WHERE id = $1`, other, url); err != nil {
		t.Fatal(err)
	}
	if got := e.get(other).Image; got == nil || got.Source != items.ImageSourceURL || got.URL != url || got.Width != 0 {
		t.Errorf("url image = %+v", got)
	}
}

func TestRepoGetForUpdateReadsStoredColumnsOnly(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, ptr("vehicle"), "Garage")
	id := e.insert(items.Item{ListID: list, Title: "Locked", Metadata: json.RawMessage(`{"make":"MV"}`),
		Price: &items.Price{Amount: ptr("7.50"), Currency: "EUR"}})
	e.setImageAttachment(id, e.attachment(10, 10, nil))

	var got items.Item
	e.tx(func(r items.Repo) error {
		var err error
		got, err = r.GetForUpdate(e.ctx, id)
		return err
	})
	if got.Title != "Locked" || got.ListID != list || got.Version != 1 || got.Price == nil || *got.Price.Min != "7.50" {
		t.Errorf("locked item = %+v", got)
	}
	if got.Type != nil || got.Image != nil || got.ContentVersion != 0 {
		t.Errorf("type, image and content version must stay unset: %+v", got)
	}

	var err error
	e.tx(func(r items.Repo) error {
		_, err = r.GetForUpdate(e.ctx, uuid.New())
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing item err = %v, want NotFound", err)
	}
}

func TestRepoUpdateWritesEditableColumns(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, ptr("vehicle"), "Garage")
	site := "https://example.com"
	id := e.insert(items.Item{ListID: list, Title: "Old", Website: &site, Metadata: json.RawMessage(`{"make":"A"}`),
		Price: &items.Price{Min: ptr("1.00"), Max: ptr("2.00"), Currency: "EUR"}})
	att := e.attachment(40, 30, nil)
	e.setImageAttachment(id, att)

	e.tx(func(r items.Repo) error {
		return r.Update(e.ctx, items.Item{ID: id, Title: "New", Metadata: json.RawMessage(`{"make":"MV","power_hp":200}`)}, later)
	})
	got := e.get(id)
	if got.Title != "New" || got.Version != 2 || got.Price != nil || got.Website != nil {
		t.Errorf("after update: %+v", got)
	}
	if !sameJSON(t, got.Metadata, json.RawMessage(`{"make":"MV","power_hp":200}`)) {
		t.Errorf("metadata = %s", got.Metadata)
	}
	if got.Image == nil || got.Image.AttachmentID != att {
		t.Errorf("image columns must not change: %+v", got.Image)
	}

	var err error
	e.tx(func(r items.Repo) error {
		err = r.Update(e.ctx, items.Item{ID: uuid.New(), Title: "x", Metadata: json.RawMessage(`{}`)}, later)
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing item err = %v, want NotFound", err)
	}
}

func TestRepoListByListActiveInOrder(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Ordered")
	other := e.insertList(nil, nil, "Other")
	c := e.sortRow(list, "C", 1)
	a := e.sortRow(list, "A", 2)
	b := e.sortRow(list, "B", 0)
	tie1 := e.sortRow(list, "T1", 5)
	tie2 := e.sortRow(list, "T2", 5)
	gone := e.sortRow(list, "Gone", 0)
	e.trash("items", gone)
	e.sortRow(other, "elsewhere", 0)

	var got []items.Item
	e.tx(func(r items.Repo) error {
		var err error
		got, err = r.ListByList(e.ctx, list)
		return err
	})
	ids := make([]uuid.UUID, len(got))
	for i, it := range got {
		ids[i] = it.ID
	}
	ties := []uuid.UUID{tie1, tie2}
	slices.SortFunc(ties, func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
	want := []uuid.UUID{b, c, a, ties[0], ties[1]}
	if !slices.Equal(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
	if len(got) > 0 && (got[0].Type != nil || got[0].ListID != list) {
		t.Errorf("first row = %+v", got[0])
	}

	emptyList := e.insertList(nil, nil, "Empty")
	var empty []items.Item
	e.tx(func(r items.Repo) error {
		var err error
		empty, err = r.ListByList(e.ctx, emptyList)
		return err
	})
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty list = %v, want an empty non-nil slice", empty)
	}
}

func TestRepoMoveAppendsToOtherList(t *testing.T) {
	e := newEnv(t)
	src := e.insertList(nil, nil, "Src")
	dst := e.insertList(nil, nil, "Dst")
	id := e.sortRow(src, "Mover", 0)

	e.tx(func(r items.Repo) error {
		return r.Move(e.ctx, id, dst, 7, later)
	})
	got := e.get(id)
	if got.ListID != dst || got.SortOrder != 7 || got.Version != 2 {
		t.Errorf("after move: list %s sort %d version %d", got.ListID, got.SortOrder, got.Version)
	}

	var err error
	e.tx(func(r items.Repo) error {
		err = r.Move(e.ctx, uuid.New(), dst, 0, later)
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing item err = %v, want NotFound", err)
	}
}

func TestRepoSetSortOrderKeepsVersion(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Sorted")
	id := e.sortRow(list, "X", 0)

	e.tx(func(r items.Repo) error {
		return r.SetSortOrder(e.ctx, id, 4, later)
	})
	got := e.get(id)
	if got.SortOrder != 4 || got.Version != 1 {
		t.Errorf("sort %d version %d, want sort 4 and version 1 (DATA_MODEL §7)", got.SortOrder, got.Version)
	}
}

func TestRepoNextSortOrder(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Next")
	var next int
	e.tx(func(r items.Repo) error {
		var err error
		next, err = r.NextSortOrder(e.ctx, list)
		return err
	})
	if next != 0 {
		t.Errorf("empty list next = %d, want 0", next)
	}

	e.sortRow(list, "a", 0)
	e.sortRow(list, "b", 4)
	e.trash("items", e.sortRow(list, "trashed", 9))
	e.tx(func(r items.Repo) error {
		var err error
		next, err = r.NextSortOrder(e.ctx, list)
		return err
	})
	if next != 5 {
		t.Errorf("next = %d, want 5 (trashed rows do not count)", next)
	}
}

func TestRepoMaxContentVersions(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Notes")
	withVersions := e.sortRow(list, "has notes", 0)
	e.addContentVersion(withVersions, 1)
	e.addContentVersion(withVersions, 2)
	e.addContentVersion(withVersions, 3)
	none := e.sortRow(list, "no notes", 1)

	// Another workspace with its own item and versions must not leak in. Bootstrap allows
	// one workspace only, so the second one is inserted directly.
	var otherWS uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO workspaces (name) VALUES ('Other') RETURNING id`).Scan(&otherWS); err != nil {
		t.Fatal(err)
	}
	otherList := uuid.New()
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO lists (id, workspace_id, name, sort_order) VALUES ($1, $2, 'O', 0)`, otherList, otherWS); err != nil {
		t.Fatal(err)
	}
	foreign := uuid.New()
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, 'foreign', 0)`, foreign, otherWS, otherList); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, 9, 'x', 1, $2)`, foreign, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	var got map[uuid.UUID]int
	e.tx(func(r items.Repo) error {
		var err error
		got, err = r.MaxContentVersions(e.ctx, []uuid.UUID{withVersions, none, foreign})
		return err
	})
	if len(got) != 1 || got[withVersions] != 3 {
		t.Errorf("max versions = %v, want only {%s: 3}", got, withVersions)
	}

	e.tx(func(r items.Repo) error {
		var err error
		got, err = r.MaxContentVersions(e.ctx, nil)
		return err
	})
	if got == nil || len(got) != 0 {
		t.Errorf("no ids = %v, want an empty map", got)
	}
}

func TestRepoListInfo(t *testing.T) {
	e := newEnv(t)
	folder := e.insertFolder(nil, "Shelf")
	list := e.insertList(&folder, ptr("book"), "Novels")
	if _, err := e.pool.Exec(e.ctx, `UPDATE lists SET icon = 'i:book', icon_color = 'red', sort_mode = 'title' WHERE id = $1`, list); err != nil {
		t.Fatal(err)
	}

	var info items.ListInfo
	e.tx(func(r items.Repo) error {
		var err error
		info, err = r.ListInfo(e.ctx, list)
		return err
	})
	if info.ID != list || info.Name != "Novels" || info.FolderID == nil || *info.FolderID != folder || info.SortMode != "title" ||
		info.Type == nil || *info.Type != "book" || info.Icon == nil || *info.Icon != "i:book" || info.IconColor == nil || *info.IconColor != "red" {
		t.Errorf("list info = %+v", info)
	}

	e.trash("lists", list)
	var err error
	e.tx(func(r items.Repo) error {
		_, err = r.ListInfo(e.ctx, list)
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("trashed list err = %v, want NotFound", err)
	}
}

func TestRepoPathOf(t *testing.T) {
	e := newEnv(t)
	root := e.insertFolder(nil, "Root")
	child := e.insertFolder(&root, "Child")
	deep := e.insertList(&child, nil, "Deep list")
	top := e.insertList(nil, nil, "Top list")

	pathOf := func(list uuid.UUID) []items.PathSegment {
		t.Helper()
		var path []items.PathSegment
		e.tx(func(r items.Repo) error {
			var err error
			path, err = r.PathOf(e.ctx, list)
			return err
		})
		return path
	}
	want := []items.PathSegment{
		{Type: "folder", ID: root, Name: "Root"},
		{Type: "folder", ID: child, Name: "Child"},
		{Type: "list", ID: deep, Name: "Deep list"},
	}
	if got := pathOf(deep); !reflect.DeepEqual(got, want) {
		t.Errorf("path = %+v, want %+v", got, want)
	}
	if got := pathOf(top); !reflect.DeepEqual(got, []items.PathSegment{{Type: "list", ID: top, Name: "Top list"}}) {
		t.Errorf("root list path = %+v", got)
	}

	// A trashed ancestor ends the chain (as the recursive query did).
	e.trash("folders", root)
	if got := pathOf(deep); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("path under a trashed parent = %+v, want %+v", got, want[1:])
	}

	var err error
	e.tx(func(r items.Repo) error {
		_, err = r.PathOf(e.ctx, uuid.New())
		return nil
	})
	if !isNotFound(err) {
		t.Errorf("missing list err = %v, want NotFound", err)
	}
}

func TestRepoLockTreeAndRollback(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Rollback")

	if err := e.repo.InTx(e.ctx, func(r items.Repo) error {
		if err := r.LockTree(e.ctx); err != nil {
			return err
		}
		if _, err := r.Insert(e.ctx, items.Item{ListID: list, Title: "never"}, now); err != nil {
			return err
		}
		return errors.New("stop")
	}); err == nil {
		t.Fatal("InTx returned nil, want the callback error")
	}

	var count int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM items WHERE list_id = $1`, list).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("rows after rollback = %d, want 0", count)
	}
}

func TestRepoInsertUsesRowTimes(t *testing.T) {
	e := newEnv(t)
	list := e.insertList(nil, nil, "Times")
	id := e.insert(items.Item{ListID: list, Title: "t", Metadata: json.RawMessage(`{}`)})
	got := e.get(id)
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Errorf("created %s updated %s, want %s (the injected clock)", got.CreatedAt, got.UpdatedAt, now)
	}
}
