package lists_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/lists"
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
	now   = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	later = now.Add(time.Hour)
)

// env is a migrated database with one bootstrapped workspace. ctx is scoped to it.
type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *lists.Postgres
	ctx   context.Context
	ws    uuid.UUID
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	return env{t: t, pool: pool, store: lists.NewPostgres(pool), ctx: scope.With(t.Context(), ws), ws: ws}
}

func ptr[T any](v T) *T { return &v }

// insertList appends a list to folder through the repository, as the service does.
func (e env) insertList(folder *uuid.UUID, name string) lists.List {
	e.t.Helper()
	var out lists.List
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		next, err := r.NextSortOrder(e.ctx, folder)
		if err != nil {
			return err
		}
		out, err = r.Insert(e.ctx, lists.List{Name: name, FolderID: folder, DisplayMode: "thumbnail", SortMode: "manual", SortOrder: next}, now)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e env) insertFolder(parent *uuid.UUID, name string, sortOrder int) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO folders (workspace_id, parent_folder_id, name, sort_order) VALUES ($1, $2, $3, $4) RETURNING id`,
		e.ws, parent, name, sortOrder).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e env) insertItem(list uuid.UUID, title string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO items (workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, list, title).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// trash marks one folder, list or item as trashed under its own batch (the trash service does more; the constraints need only this).
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

func (e env) childIDs(folder *uuid.UUID) []uuid.UUID {
	e.t.Helper()
	var ids []uuid.UUID
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		var err error
		ids, err = r.ChildIDs(e.ctx, folder)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return ids
}

func (e env) get(id uuid.UUID) (lists.List, error) {
	return e.store.Get(e.ctx, id)
}

func TestRepo_InsertAndGet(t *testing.T) {
	e := newEnv(t)
	in := lists.List{
		Name: "Books", Type: ptr("book"), DisplayMode: "grid", SortMode: "title",
		Description: ptr("# Read"),
		Icon:        ptr("i:book"), IconColor: ptr("blue"), SortOrder: 0,
	}
	var id uuid.UUID
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		out, err := r.Insert(e.ctx, in, now)
		id = out.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.get(id)
	if err != nil {
		t.Fatal(err)
	}
	want := lists.List{
		ID: id, Name: "Books", Type: ptr("book"), DisplayMode: "grid", SortMode: "title",
		Description: ptr("# Read"),
		Icon:        ptr("i:book"), IconColor: ptr("blue"), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

func TestRepo_GetMissingIsNotFound(t *testing.T) {
	e := newEnv(t)
	trashed := e.insertList(nil, "Gone")
	e.trash("lists", trashed.ID)
	for _, id := range []uuid.UUID{uuid.New(), trashed.ID} {
		if _, err := e.get(id); !isNotFound(err, "list") {
			t.Errorf("Get(%s) err = %v, want NotFound list", id, err)
		}
		err := e.store.InTx(e.ctx, func(r lists.Repo) error {
			_, err := r.GetForUpdate(e.ctx, id)
			return err
		})
		if !isNotFound(err, "list") {
			t.Errorf("GetForUpdate(%s) err = %v, want NotFound list", id, err)
		}
	}
}

func TestRepo_UpdateAndType(t *testing.T) {
	e := newEnv(t)
	l := e.insertList(nil, "Books")
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		cur, err := r.GetForUpdate(e.ctx, l.ID)
		if err != nil {
			return err
		}
		cur.Name = "Novels"
		cur.Description = ptr("notes")
		cur.Icon = ptr("i:book")
		cur.IconColor = ptr("red")
		cur.Type = ptr("book")
		// The type is written by UpdateType, the rest by Update.
		if _, err := r.UpdateType(e.ctx, l.ID, cur.Type, later); err != nil {
			return err
		}
		out, err := r.Update(e.ctx, cur, later)
		if err != nil {
			return err
		}
		if out.Version != 3 {
			t.Errorf("version after UpdateType and Update = %d, want 3", out.Version)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.get(l.ID)
	if got.Name != "Novels" || got.Description == nil || *got.Description != "notes" || got.Icon == nil || *got.Icon != "i:book" ||
		got.IconColor == nil || *got.IconColor != "red" || got.Type == nil || *got.Type != "book" || !got.UpdatedAt.Equal(later) {
		t.Errorf("after update = %+v", got)
	}
}

func TestRepo_UpdateTypeClearsType(t *testing.T) {
	e := newEnv(t)
	var id uuid.UUID
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		out, err := r.Insert(e.ctx, lists.List{Name: "W", Type: ptr("wishlist"), DisplayMode: "thumbnail", SortMode: "manual"}, now)
		id = out.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var got lists.List
	err = e.store.InTx(e.ctx, func(r lists.Repo) error {
		var err error
		got, err = r.UpdateType(e.ctx, id, nil, later)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != nil || got.Version != 2 {
		t.Errorf("UpdateType(nil) = %+v, want no type, version 2", got)
	}
}

func TestRepo_ChildrenSortOrderAndNext(t *testing.T) {
	e := newEnv(t)
	folder := e.insertFolder(nil, "F", 0)
	a := e.insertList(nil, "A")
	b := e.insertList(nil, "B")
	c := e.insertList(nil, "C")
	d := e.insertList(&folder, "D")
	if got := e.childIDs(nil); !reflect.DeepEqual(got, []uuid.UUID{a.ID, b.ID, c.ID}) {
		t.Errorf("root children = %v, want A B C", got)
	}
	if got := e.childIDs(&folder); !reflect.DeepEqual(got, []uuid.UUID{d.ID}) {
		t.Errorf("folder children = %v, want [D]", got)
	}
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		if next, err := r.NextSortOrder(e.ctx, nil); err != nil || next != 3 {
			t.Errorf("next at root = %d, %v; want 3", next, err)
		}
		if next, err := r.NextSortOrder(e.ctx, &folder); err != nil || next != 1 {
			t.Errorf("next in folder = %d, %v; want 1", next, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Positions are rewritten without bumping versions (DATA_MODEL §7).
	err = e.store.InTx(e.ctx, func(r lists.Repo) error {
		for i, id := range []uuid.UUID{c.ID, a.ID, b.ID} {
			if err := r.SetSortOrder(e.ctx, id, i, later); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.childIDs(nil); !reflect.DeepEqual(got, []uuid.UUID{c.ID, a.ID, b.ID}) {
		t.Errorf("root children after reorder = %v, want C A B", got)
	}
	if got, _ := e.get(a.ID); got.SortOrder != 1 || got.Version != 1 {
		t.Errorf("A after reorder = sort %d version %d, want sort 1 version 1", got.SortOrder, got.Version)
	}
}

func TestRepo_MoveAppendsToFolder(t *testing.T) {
	e := newEnv(t)
	folder := e.insertFolder(nil, "F", 0)
	d := e.insertList(&folder, "D")
	b := e.insertList(nil, "B")
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		next, err := r.NextSortOrder(e.ctx, &folder)
		if err != nil {
			return err
		}
		out, err := r.Move(e.ctx, b.ID, &folder, next, later)
		if err != nil {
			return err
		}
		if out.FolderID == nil || *out.FolderID != folder || out.SortOrder != 1 || out.Version != 2 {
			t.Errorf("Move = %+v, want in folder at 1, version 2", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.childIDs(&folder); !reflect.DeepEqual(got, []uuid.UUID{d.ID, b.ID}) {
		t.Errorf("folder children = %v, want D B", got)
	}
	if got := e.childIDs(nil); len(got) != 0 {
		t.Errorf("root children = %v, want none", got)
	}
	// Moving back to the root appends after the root's children.
	err = e.store.InTx(e.ctx, func(r lists.Repo) error {
		next, err := r.NextSortOrder(e.ctx, nil)
		if err != nil {
			return err
		}
		_, err = r.Move(e.ctx, b.ID, nil, next, later)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.childIDs(nil); !reflect.DeepEqual(got, []uuid.UUID{b.ID}) {
		t.Errorf("root children after moving back = %v, want B", got)
	}
}

func TestRepo_ListActiveWithItemCount(t *testing.T) {
	e := newEnv(t)
	first := e.insertList(nil, "First")
	second := e.insertList(nil, "Second")
	gone := e.insertList(nil, "Gone")
	e.insertItem(first.ID, "one")
	e.insertItem(first.ID, "two")
	trashed := e.insertItem(first.ID, "three")
	e.insertItem(gone.ID, "orphan")
	e.trash("items", trashed)
	e.trash("lists", gone.ID)

	var active []lists.List
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		var err error
		active, err = r.ListActive(e.ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].ID != first.ID || active[1].ID != second.ID {
		t.Fatalf("ListActive = %+v, want First and Second", active)
	}
	if active[0].ItemCount != 2 || active[1].ItemCount != 0 {
		t.Errorf("item counts = %d, %d; want 2 active, 0 (trashed items and lists excluded)", active[0].ItemCount, active[1].ItemCount)
	}
	got, err := e.get(first.ID)
	if err != nil || got.ItemCount != 2 {
		t.Errorf("Get item count = %d, %v; want 2", got.ItemCount, err)
	}
}

func TestRepo_FolderChain(t *testing.T) {
	e := newEnv(t)
	root := e.insertFolder(nil, "Root", 0)
	mid := e.insertFolder(&root, "Mid", 0)
	leaf := e.insertFolder(&mid, "Leaf", 0)
	trashedMid := e.insertFolder(&root, "Trashed mid", 1)
	underTrashed := e.insertFolder(&trashedMid, "Under trashed", 0)
	e.trash("folders", trashedMid)
	gone := e.insertFolder(nil, "Gone", 1)
	e.trash("folders", gone)

	tests := []struct {
		name   string
		folder uuid.UUID
		want   []string
	}{
		{"root is its own path", root, []string{"Root"}},
		{"ancestors root first", leaf, []string{"Root", "Mid", "Leaf"}},
		{"a trashed ancestor ends the chain", underTrashed, []string{"Under trashed"}},
		{"a trashed folder has no path", gone, nil},
		{"a missing folder has no path", uuid.New(), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []lists.PathFolder
			err := e.store.InTx(e.ctx, func(r lists.Repo) error {
				var err error
				got, err = r.FolderChain(e.ctx, tt.folder)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, p := range got {
				names = append(names, p.Name)
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("chain = %v, want %v", names, tt.want)
			}
		})
	}
}

// A corrupt cycle (A under B, B under A) must not hang the walk.
func TestRepo_FolderChainCycleTerminates(t *testing.T) {
	e := newEnv(t)
	a := e.insertFolder(nil, "A", 0)
	b := e.insertFolder(&a, "B", 0)
	if _, err := e.pool.Exec(e.ctx, `UPDATE folders SET parent_folder_id = $2 WHERE id = $1`, a, b); err != nil {
		t.Fatal(err)
	}
	var got []lists.PathFolder
	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		var err error
		got, err = r.FolderChain(e.ctx, b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a || got[1].ID != b {
		t.Errorf("chain = %+v, want A then B once each", got)
	}
}

func TestRepo_FolderExists(t *testing.T) {
	e := newEnv(t)
	active := e.insertFolder(nil, "Active", 0)
	trashed := e.insertFolder(nil, "Trashed", 1)
	e.trash("folders", trashed)
	for _, tt := range []struct {
		name string
		id   uuid.UUID
		want bool
	}{{"active", active, true}, {"trashed", trashed, false}, {"missing", uuid.New(), false}} {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			err := e.store.InTx(e.ctx, func(r lists.Repo) error {
				var err error
				got, err = r.FolderExists(e.ctx, tt.id)
				return err
			})
			if err != nil || got != tt.want {
				t.Errorf("FolderExists = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// SEAM methods (move to items): a type change clears the metadata and,
// when the new type has no price, the price of every item of the list, trashed ones included.
func TestRepo_ClearItemsMetadataAndPrice(t *testing.T) {
	e := newEnv(t)
	l := e.insertList(nil, "Shop")
	active := e.insertItem(l.ID, "active")
	trashed := e.insertItem(l.ID, "trashed")
	e.trash("items", trashed)
	other := e.insertList(nil, "Other")
	untouched := e.insertItem(other.ID, "untouched")
	if _, err := e.pool.Exec(e.ctx, `UPDATE items SET metadata = '{"make":"Ducati"}', price_min = 10, price_max = 12, currency = 'EUR'
		WHERE id = ANY($1)`, []uuid.UUID{active, trashed, untouched}); err != nil {
		t.Fatal(err)
	}

	err := e.store.InTx(e.ctx, func(r lists.Repo) error {
		if err := r.ClearItemsMetadata(e.ctx, l.ID, later); err != nil {
			return err
		}
		return r.ClearItemsPrice(e.ctx, l.ID, later)
	})
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		metadata string
		price    *string
		version  int
	}
	read := func(id uuid.UUID) row {
		var out row
		if err := e.pool.QueryRow(e.ctx, `SELECT metadata::text, price_min::text, version FROM items WHERE id = $1`, id).
			Scan(&out.metadata, &out.price, &out.version); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, id := range []uuid.UUID{active, trashed} {
		if got := read(id); got.metadata != "{}" || got.price != nil || got.version != 3 {
			t.Errorf("item %s = %+v, want metadata {}, no price, version 3 (one bump per clear)", id, got)
		}
	}
	if got := read(untouched); got.metadata == "{}" || got.price == nil || got.version != 1 {
		t.Errorf("item of another list = %+v, want it unchanged", got)
	}
}

func isNotFound(err error, resource string) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf) && nf.Resource == resource
}
