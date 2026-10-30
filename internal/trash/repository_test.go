package trash_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/trash"
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
	repo *trash.Postgres
	ctx  context.Context
	ws   uuid.UUID
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	return env{t: t, pool: pool, repo: trash.NewPostgres(pool), ctx: scope.With(t.Context(), ws), ws: ws}
}

// in runs fn in one transaction through the repository. The test checks fn's error.
func (e env) in(fn func(r trash.Repo) error) error {
	return e.repo.InTx(e.ctx, fn)
}

// must runs fn in one transaction and fails the test on error.
func (e env) must(fn func(r trash.Repo) error) {
	e.t.Helper()
	if err := e.in(fn); err != nil {
		e.t.Fatal(err)
	}
}

// folder inserts an active folder at sort order 0.
func (e env) folder(name string, parent *uuid.UUID) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO folders (workspace_id, parent_folder_id, name, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, parent, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// list inserts an active list at sort order 0.
func (e env) list(name string, folder *uuid.UUID) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO lists (workspace_id, folder_id, name, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, folder, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// item inserts an active item at sort order 0.
func (e env) item(list uuid.UUID, title string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO items (workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, 0) RETURNING id`,
		e.ws, list, title).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// batch inserts a trash batch with the JSON former path given, trashed at at.
func (e env) batch(rootType string, root uuid.UUID, name string, at time.Time, path string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path, deleted_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6) RETURNING id`, e.ws, rootType, root, name, path, at).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// trash marks one row of table (folders, lists or items) as part of batch, the way a delete does.
func (e env) trash(table string, id, batch uuid.UUID, at time.Time) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, `UPDATE `+table+` SET deleted_at = $2, trash_batch_id = $3, updated_at = $2 WHERE id = $1`, id, at, batch); err != nil {
		e.t.Fatal(err)
	}
}

// count runs a count query.
func (e env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// state returns the version, updated_at and trash columns of one row of table.
func (e env) state(table string, id uuid.UUID) (version int, updated time.Time, deleted *time.Time, batch uuid.NullUUID) {
	e.t.Helper()
	if err := e.pool.QueryRow(e.ctx, `SELECT version, updated_at, deleted_at, trash_batch_id FROM `+table+` WHERE id = $1`, id).
		Scan(&version, &updated, &deleted, &batch); err != nil {
		e.t.Fatal(err)
	}
	return version, updated, deleted, batch
}

// otherWorkspace inserts a second workspace and returns a context scoped to it.
func (e env) otherWorkspace() (uuid.UUID, context.Context) {
	e.t.Helper()
	var ws uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO workspaces (name) VALUES ('Other') RETURNING id`).Scan(&ws); err != nil {
		e.t.Fatal(err)
	}
	return ws, scope.With(e.t.Context(), ws)
}

func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}

func TestRepoLockTreeAndInTxRollback(t *testing.T) {
	e := newEnv(t)
	b := uuid.New()

	// A tree lock inside a transaction works, and an error rolls the batch insert back.
	err := e.in(func(r trash.Repo) error {
		if err := r.LockTree(e.ctx); err != nil {
			return err
		}
		if _, err := r.InsertBatch(e.ctx, trash.TypeFolder, b, "Gone", nil, now); err != nil {
			return err
		}
		return errors.New("stop")
	})
	if err == nil {
		t.Fatal("InTx returned nil, want the callback error")
	}
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 0 {
		t.Fatalf("batches after rollback = %d, want 0", n)
	}
}

func TestRepoInsertAndLockBatch(t *testing.T) {
	e := newEnv(t)
	root := e.folder("Dune", nil)
	path := []trash.PathSegment{{Type: trash.TypeFolder, ID: uuid.New(), Name: "Old"}}

	var id uuid.UUID
	e.must(func(r trash.Repo) (err error) {
		id, err = r.InsertBatch(e.ctx, trash.TypeFolder, root, "Dune", path, now)
		return err
	})

	var got trash.Batch
	e.must(func(r trash.Repo) (err error) {
		got, err = r.LockBatch(e.ctx, id)
		return err
	})
	if got.ID != id || got.RootType != trash.TypeFolder || got.RootID != root || got.RootName != "Dune" || !got.DeletedAt.Equal(now) {
		t.Errorf("batch = %+v, want the inserted one at %s", got, now)
	}
	var stored string
	if err := e.pool.QueryRow(e.ctx, `SELECT former_path::text FROM trash_batches WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `[{"id": "`+path[0].ID.String()+`", "name": "Old", "type": "folder"}]` {
		t.Errorf("former_path = %s", stored)
	}

	err := e.in(func(r trash.Repo) error {
		_, err := r.LockBatch(e.ctx, uuid.New())
		return err
	})
	if !isNotFound(err) {
		t.Errorf("missing batch err = %v, want NotFound", err)
	}
	_, otherCtx := e.otherWorkspace()
	err = e.repo.InTx(otherCtx, func(r trash.Repo) error {
		_, err := r.LockBatch(otherCtx, id)
		return err
	})
	if !isNotFound(err) {
		t.Errorf("batch of another workspace err = %v, want NotFound", err)
	}
}

func TestRepoListBatchesAndCount(t *testing.T) {
	e := newEnv(t)
	oldPath := `[{"type": "folder", "id": "` + uuid.NewString() + `", "name": "Old"}]`

	// P (trashed first) holds nothing; Q under P is trashed later and cannot be restored
	// in place because P is in the trash; F is the newest and has a list and an item.
	p := e.folder("P", nil)
	q := e.folder("Q", &p)
	f := e.folder("F", nil)
	l := e.list("L", &f)
	i := e.item(l, "I")
	bP := e.batch(trash.TypeFolder, p, "P", now.Add(-3*time.Hour), `[]`)
	bQ := e.batch(trash.TypeFolder, q, "Q", now.Add(-2*time.Hour), `[]`)
	bF := e.batch(trash.TypeFolder, f, "F", now.Add(-time.Hour), oldPath)
	e.trash("folders", p, bP, now.Add(-3*time.Hour))
	e.trash("folders", q, bQ, now.Add(-2*time.Hour))
	e.trash("folders", f, bF, now.Add(-time.Hour))
	e.trash("lists", l, bF, now.Add(-time.Hour))
	e.trash("items", i, bF, now.Add(-time.Hour))

	var page []trash.Entry
	var total int
	e.must(func(r trash.Repo) (err error) {
		page, err = r.ListBatches(e.ctx, 2, 0)
		return err
	})
	if len(page) != 2 {
		t.Fatalf("page 1 = %+v, want 2 entries", page)
	}
	if page[0].ID != bF || page[1].ID != bQ {
		t.Fatalf("order = %v, %v; want F then Q (newest first)", page[0].ID, page[1].ID)
	}
	fe := page[0]
	if fe.Folders != 1 || fe.Lists != 1 || fe.Items != 1 || !fe.RestorableInPlace || fe.RootName != "F" {
		t.Errorf("F entry = %+v, want counts 1/1/1, restorable in place", fe)
	}
	if len(fe.FormerPath) != 1 || fe.FormerPath[0].Name != "Old" || fe.FormerPath[0].Type != trash.TypeFolder {
		t.Errorf("F former path = %+v", fe.FormerPath)
	}
	if qe := page[1]; qe.RestorableInPlace || qe.Folders != 1 || qe.FormerPath == nil || len(qe.FormerPath) != 0 {
		t.Errorf("Q entry = %+v, want not restorable in place and an empty path", qe)
	}

	e.must(func(r trash.Repo) (err error) {
		page, err = r.ListBatches(e.ctx, 2, 2)
		return err
	})
	if len(page) != 1 || page[0].ID != bP || !page[0].RestorableInPlace {
		t.Errorf("page 2 = %+v, want P only, restorable in place", page)
	}

	e.must(func(r trash.Repo) (err error) {
		total, err = r.CountBatches(e.ctx)
		return err
	})
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
}

func TestRepoDeleteBatchAndOrphans(t *testing.T) {
	e := newEnv(t)
	kept := e.folder("Kept", nil)
	bKept := e.batch(trash.TypeFolder, kept, "Kept", now, `[]`)
	// Batches whose root row is gone, one per root type.
	orphans := []uuid.UUID{
		e.batch(trash.TypeFolder, uuid.New(), "gone folder", now, `[]`),
		e.batch(trash.TypeList, uuid.New(), "gone list", now, `[]`),
		e.batch(trash.TypeItem, uuid.New(), "gone item", now, `[]`),
	}

	e.must(func(r trash.Repo) error { return r.DeleteOrphanBatches(e.ctx) })
	if n := e.count(`SELECT count(*) FROM trash_batches`); n != 1 {
		t.Fatalf("batches after orphan sweep = %d, want the kept one", n)
	}
	err := e.in(func(r trash.Repo) error {
		_, err := r.LockBatch(e.ctx, orphans[0])
		return err
	})
	if !isNotFound(err) {
		t.Errorf("orphan err = %v, want NotFound", err)
	}

	e.must(func(r trash.Repo) error { return r.DeleteBatch(e.ctx, bKept) })
	if n := e.count(`SELECT count(*) FROM trash_batches WHERE id = $1`, bKept); n != 0 {
		t.Errorf("deleted batch rows = %d, want 0", n)
	}
}

func TestRepoBatchIDsAndExpired(t *testing.T) {
	e := newEnv(t)
	old := e.batch(trash.TypeFolder, uuid.New(), "old", now.Add(-8*24*time.Hour), `[]`)
	recent := e.batch(trash.TypeFolder, uuid.New(), "recent", now.Add(-6*24*time.Hour), `[]`)
	_, otherCtx := e.otherWorkspace()
	var other uuid.UUID
	if err := e.pool.QueryRow(otherCtx, `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path, deleted_at)
		SELECT id, 'folder', gen_random_uuid(), 'foreign', '[]', $1 FROM workspaces WHERE name = 'Other' RETURNING id`, now.Add(-8*24*time.Hour)).Scan(&other); err != nil {
		t.Fatal(err)
	}

	var ids []uuid.UUID
	e.must(func(r trash.Repo) (err error) {
		ids, err = r.BatchIDs(e.ctx)
		return err
	})
	if !slices.Contains(ids, old) || !slices.Contains(ids, recent) || slices.Contains(ids, other) || len(ids) != 2 {
		t.Errorf("batch ids = %v, want the two of this workspace", ids)
	}

	var expired []uuid.UUID
	e.must(func(r trash.Repo) (err error) {
		expired, err = r.ExpiredBatchIDs(e.ctx, now.Add(-7*24*time.Hour))
		return err
	})
	if len(expired) != 1 || expired[0] != old {
		t.Errorf("expired = %v, want only %s", expired, old)
	}
}

func TestRepoRetentionDays(t *testing.T) {
	e := newEnv(t)
	read := func() *int {
		t.Helper()
		var days *int
		e.must(func(r trash.Repo) (err error) {
			days, err = r.RetentionDays(e.ctx)
			return err
		})
		return days
	}

	// Bootstrap writes the settings row with retention NULL (off).
	if days := read(); days != nil {
		t.Errorf("retention NULL: days = %d, want nil", *days)
	}
	e.exec(`UPDATE workspace_settings SET trash_retention_days = 7 WHERE workspace_id = $1`, e.ws)
	if days := read(); days == nil || *days != 7 {
		t.Errorf("retention 7: days = %v, want 7", days)
	}
	e.exec(`DELETE FROM workspace_settings WHERE workspace_id = $1`, e.ws)
	if days := read(); days != nil {
		t.Errorf("no settings row: days = %d, want nil", *days)
	}
}

// exec runs a statement that returns nothing.
func (e env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestRepoFolderParentsAnyState(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	g := e.folder("G", &f)
	e.trash("folders", g, e.batch(trash.TypeFolder, g, "G", now, `[]`), now)
	_, otherCtx := e.otherWorkspace()
	if _, err := e.pool.Exec(otherCtx, `INSERT INTO folders (workspace_id, name, sort_order) SELECT id, 'X', 0 FROM workspaces WHERE name = 'Other'`); err != nil {
		t.Fatal(err)
	}

	var nodes []trash.FolderNode
	e.must(func(r trash.Repo) (err error) {
		nodes, err = r.FolderParents(e.ctx)
		return err
	})
	if len(nodes) != 2 {
		t.Fatalf("nodes = %+v, want F and G only", nodes)
	}
	byID := map[uuid.UUID]trash.FolderNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if n := byID[f]; n.Name != "F" || n.Trashed || n.ParentID != nil {
		t.Errorf("F = %+v", n)
	}
	if n := byID[g]; n.Name != "G" || !n.Trashed || n.ParentID == nil || *n.ParentID != f {
		t.Errorf("G = %+v, want trashed under F", n)
	}
}

func TestRepoLocksAndActiveReads(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", &f)
	it := e.item(l, "I")
	tf := e.folder("TF", nil)
	tl := e.list("TL", nil)
	ti := e.item(l, "TI")
	bTF := e.batch(trash.TypeFolder, tf, "TF", now, `[]`)
	bTL := e.batch(trash.TypeList, tl, "TL", now, `[]`)
	bTI := e.batch(trash.TypeItem, ti, "TI", now, `[]`)
	e.trash("folders", tf, bTF, now)
	e.trash("lists", tl, bTL, now)
	e.trash("items", ti, bTI, now)

	// Active rows lock and read; trashed rows are NotFound for the locking and active reads.
	e.must(func(r trash.Repo) error {
		fr, err := r.LockFolder(e.ctx, f)
		if err != nil || fr.ID != f || fr.Name != "F" || fr.Version != 1 || fr.ParentID != nil {
			t.Errorf("LockFolder = %+v, %v", fr, err)
		}
		lr, err := r.LockList(e.ctx, l)
		if err != nil || lr.ID != l || lr.Name != "L" || lr.FolderID == nil || *lr.FolderID != f || lr.Version != 1 {
			t.Errorf("LockList = %+v, %v", lr, err)
		}
		ir, err := r.LockItem(e.ctx, it)
		if err != nil || ir.ID != it || ir.Title != "I" || ir.ListID != l || ir.Version != 1 {
			t.Errorf("LockItem = %+v, %v", ir, err)
		}
		af, err := r.GetActiveFolder(e.ctx, f)
		if err != nil || af.ID != f {
			t.Errorf("GetActiveFolder = %+v, %v", af, err)
		}
		al, err := r.GetActiveList(e.ctx, l)
		if err != nil || al.ID != l || al.Name != "L" || al.FolderID == nil || *al.FolderID != f {
			t.Errorf("GetActiveList = %+v, %v", al, err)
		}
		return nil
	})

	e.must(func(r trash.Repo) error {
		if _, err := r.LockFolder(e.ctx, tf); !isNotFound(err) {
			t.Errorf("LockFolder(trashed) err = %v, want NotFound", err)
		}
		if _, err := r.LockList(e.ctx, tl); !isNotFound(err) {
			t.Errorf("LockList(trashed) err = %v, want NotFound", err)
		}
		if _, err := r.LockItem(e.ctx, ti); !isNotFound(err) {
			t.Errorf("LockItem(trashed) err = %v, want NotFound", err)
		}
		if _, err := r.GetActiveFolder(e.ctx, tf); !isNotFound(err) {
			t.Errorf("GetActiveFolder(trashed) err = %v, want NotFound", err)
		}
		if _, err := r.GetActiveList(e.ctx, tl); !isNotFound(err) {
			t.Errorf("GetActiveList(trashed) err = %v, want NotFound", err)
		}
		if _, err := r.LockFolder(e.ctx, uuid.New()); !isNotFound(err) {
			t.Errorf("LockFolder(missing) err = %v, want NotFound", err)
		}
		return nil
	})

	// Any-state reads see the trashed rows with their trashed flag.
	e.must(func(r trash.Repo) error {
		fs, err := r.GetAnyFolderState(e.ctx, tf)
		if err != nil || fs.Name != "TF" || !fs.Trashed {
			t.Errorf("GetAnyFolderState(trashed) = %+v, %v", fs, err)
		}
		ls, err := r.GetAnyListState(e.ctx, tl)
		if err != nil || ls.Name != "TL" || !ls.Trashed {
			t.Errorf("GetAnyListState(trashed) = %+v, %v", ls, err)
		}
		fa, err := r.GetAnyFolderState(e.ctx, f)
		if err != nil || fa.Trashed {
			t.Errorf("GetAnyFolderState(active) = %+v, %v", fa, err)
		}
		if _, err := r.GetAnyFolderState(e.ctx, uuid.New()); !isNotFound(err) {
			t.Errorf("GetAnyFolderState(missing) err = %v, want NotFound", err)
		}
		if _, err := r.GetAnyListState(e.ctx, uuid.New()); !isNotFound(err) {
			t.Errorf("GetAnyListState(missing) err = %v, want NotFound", err)
		}
		return nil
	})
}

func TestRepoGetTrashedRoots(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", nil)
	it := e.item(l, "I")
	active := e.folder("Active", nil)
	bF := e.batch(trash.TypeFolder, f, "F", now, `[]`)
	bL := e.batch(trash.TypeList, l, "L", now, `[]`)
	bI := e.batch(trash.TypeItem, it, "I", now, `[]`)
	e.trash("folders", f, bF, now)
	e.trash("lists", l, bL, now)
	e.trash("items", it, bI, now)
	other := e.batch(trash.TypeFolder, uuid.New(), "other", now, `[]`)

	e.must(func(r trash.Repo) error {
		if fr, err := r.GetTrashedFolder(e.ctx, f, bF); err != nil || fr.ID != f || fr.Name != "F" {
			t.Errorf("GetTrashedFolder = %+v, %v", fr, err)
		}
		if lr, err := r.GetTrashedList(e.ctx, l, bL); err != nil || lr.ID != l || lr.Name != "L" {
			t.Errorf("GetTrashedList = %+v, %v", lr, err)
		}
		if ir, err := r.GetTrashedItem(e.ctx, it, bI); err != nil || ir.ID != it || ir.ListID != l {
			t.Errorf("GetTrashedItem = %+v, %v", ir, err)
		}
		// Another batch, or an active row, is not this batch's root.
		if _, err := r.GetTrashedFolder(e.ctx, f, other); !isNotFound(err) {
			t.Errorf("folder with another batch err = %v, want NotFound", err)
		}
		if _, err := r.GetTrashedList(e.ctx, l, other); !isNotFound(err) {
			t.Errorf("list with another batch err = %v, want NotFound", err)
		}
		if _, err := r.GetTrashedItem(e.ctx, it, other); !isNotFound(err) {
			t.Errorf("item with another batch err = %v, want NotFound", err)
		}
		if _, err := r.GetTrashedFolder(e.ctx, active, bF); !isNotFound(err) {
			t.Errorf("active folder err = %v, want NotFound", err)
		}
		return nil
	})
}

func TestRepoNextSortOrder(t *testing.T) {
	e := newEnv(t)
	p := e.folder("P", nil)
	e.folder("C1", &p)
	c2 := e.folder("C2", &p)
	e.exec(`UPDATE folders SET sort_order = 4 WHERE id = $1`, c2)
	root := e.folder("Root", nil)
	e.exec(`UPDATE folders SET sort_order = 2 WHERE id = $1`, root)
	l := e.list("L", nil)
	e.exec(`UPDATE lists SET sort_order = 6 WHERE id = $1`, l)
	e.item(l, "I1")
	it2 := e.item(l, "I2")
	e.exec(`UPDATE items SET sort_order = 7 WHERE id = $1`, it2)

	check := func(name string, get func(r trash.Repo) (int, error), want int) {
		t.Helper()
		var got int
		e.must(func(r trash.Repo) (err error) {
			got, err = get(r)
			return err
		})
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("folder next under P", func(r trash.Repo) (int, error) { return r.NextFolderSortOrder(e.ctx, &p) }, 5)
	check("folder next at root", func(r trash.Repo) (int, error) { return r.NextFolderSortOrder(e.ctx, nil) }, 3)
	check("list next at root", func(r trash.Repo) (int, error) { return r.NextListSortOrder(e.ctx, nil) }, 7)
	check("item next in list", func(r trash.Repo) (int, error) { return r.NextItemSortOrder(e.ctx, l) }, 8)

	// A trashed sibling does not count.
	e.trash("folders", c2, e.batch(trash.TypeFolder, c2, "C2", now, `[]`), now)
	check("folder next under P after trashing C2", func(r trash.Repo) (int, error) { return r.NextFolderSortOrder(e.ctx, &p) }, 1)
}

func TestRepoMarkRowsAndActiveIDs(t *testing.T) {
	e := newEnv(t)
	a := e.folder("A", nil)
	b := e.folder("B", &a)
	x := e.folder("X", nil) // trashed by an older batch: must keep that batch
	oldBatch := e.batch(trash.TypeFolder, x, "X", now.Add(-time.Hour), `[]`)
	e.trash("folders", x, oldBatch, now.Add(-time.Hour))
	l := e.list("L", &a)
	i1 := e.item(l, "I1")
	i2 := e.item(l, "I2")
	batch := e.batch(trash.TypeFolder, a, "A", later, `[]`)

	e.must(func(r trash.Repo) error {
		return r.MarkFolders(e.ctx, []uuid.UUID{a, b, x}, batch, later)
	})
	for _, id := range []uuid.UUID{a, b} {
		version, updated, deleted, got := e.state("folders", id)
		if version != 1 || !updated.Equal(later) || deleted == nil || !deleted.Equal(later) || got != (uuid.NullUUID{UUID: batch, Valid: true}) {
			t.Errorf("folder %s after mark: version %d updated %s deleted %v batch %v", id, version, updated, deleted, got)
		}
	}
	if _, _, deleted, got := e.state("folders", x); deleted == nil || !deleted.Equal(now.Add(-time.Hour)) || got.UUID != oldBatch {
		t.Errorf("already trashed folder was re-marked: deleted %v batch %v", deleted, got)
	}

	var lists []uuid.UUID
	e.must(func(r trash.Repo) (err error) {
		lists, err = r.ActiveListIDsByFolders(e.ctx, []uuid.UUID{a, b})
		return err
	})
	if len(lists) != 1 || lists[0] != l {
		t.Fatalf("active lists = %v, want %s", lists, l)
	}
	e.must(func(r trash.Repo) error { return r.MarkLists(e.ctx, lists, batch, later) })
	e.must(func(r trash.Repo) (err error) {
		lists, err = r.ActiveListIDsByFolders(e.ctx, []uuid.UUID{a, b})
		return err
	})
	if len(lists) != 0 {
		t.Errorf("active lists after mark = %v, want none", lists)
	}

	var items []uuid.UUID
	e.must(func(r trash.Repo) (err error) {
		items, err = r.ActiveItemIDsByLists(e.ctx, []uuid.UUID{l})
		return err
	})
	if len(items) != 2 {
		t.Fatalf("active items = %v, want both", items)
	}
	e.must(func(r trash.Repo) error { return r.MarkItems(e.ctx, []uuid.UUID{i1}, batch, later) })
	e.must(func(r trash.Repo) (err error) {
		items, err = r.ActiveItemIDsByLists(e.ctx, []uuid.UUID{l})
		return err
	})
	if len(items) != 1 || items[0] != i2 {
		t.Errorf("active items after marking I1 = %v, want only %s", items, i2)
	}
	if _, _, deleted, got := e.state("items", i1); deleted == nil || got.UUID != batch {
		t.Errorf("item I1 not in batch: deleted %v batch %v", deleted, got)
	}
	if _, _, _, got := e.state("lists", l); got.UUID != batch {
		t.Errorf("list L not in batch: %v", got)
	}
}

func TestRepoRestoreFolderRootAndBatch(t *testing.T) {
	e := newEnv(t)
	p := e.folder("P", nil)
	c := e.folder("C", &p)
	l := e.list("L", &c)
	it := e.item(l, "I")
	b := e.batch(trash.TypeFolder, p, "P", now, `[]`)
	e.trash("folders", p, b, now)
	e.trash("folders", c, b, now)
	e.trash("lists", l, b, now)
	e.trash("items", it, b, now)

	e.must(func(r trash.Repo) error {
		return r.RestoreFolderRoot(e.ctx, p, b, nil, 5, later)
	})
	version, updated, deleted, got := e.state("folders", p)
	if version != 2 || !updated.Equal(later) || deleted != nil || got.Valid {
		t.Errorf("root after restore: version %d updated %s deleted %v batch %v", version, updated, deleted, got)
	}
	if n := e.count(`SELECT sort_order FROM folders WHERE id = $1`, p); n != 5 {
		t.Errorf("root sort order = %d, want 5", n)
	}

	// The rest of the batch comes back with its versions unchanged.
	e.must(func(r trash.Repo) error {
		if err := r.RestoreBatchFolders(e.ctx, b, later); err != nil {
			return err
		}
		if err := r.RestoreBatchLists(e.ctx, b, later); err != nil {
			return err
		}
		return r.RestoreBatchItems(e.ctx, b, later)
	})
	for _, tc := range []struct {
		table string
		id    uuid.UUID
	}{{"folders", c}, {"lists", l}, {"items", it}} {
		version, _, deleted, got := e.state(tc.table, tc.id)
		if version != 1 || deleted != nil || got.Valid {
			t.Errorf("%s %s after batch restore: version %d deleted %v batch %v", tc.table, tc.id, version, deleted, got)
		}
	}
}

func TestRepoRestoreListAndItemRoots(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", nil)
	it := e.item(e.list("Other", nil), "I")
	bL := e.batch(trash.TypeList, l, "L", now, `[]`)
	bI := e.batch(trash.TypeItem, it, "I", now, `[]`)
	e.trash("lists", l, bL, now)
	e.trash("items", it, bI, now)
	target := e.list("Target", nil)

	e.must(func(r trash.Repo) error {
		if err := r.RestoreListRoot(e.ctx, l, bL, &f, 3, later); err != nil {
			return err
		}
		return r.RestoreItemRoot(e.ctx, it, bI, target, 4, later)
	})
	version, _, deleted, got := e.state("lists", l)
	if version != 2 || deleted != nil || got.Valid || e.count(`SELECT count(*) FROM lists WHERE id = $1 AND folder_id = $2 AND sort_order = 3`, l, f) != 1 {
		t.Errorf("list root after restore: version %d deleted %v batch %v", version, deleted, got)
	}
	version, _, deleted, got = e.state("items", it)
	if version != 2 || deleted != nil || got.Valid || e.count(`SELECT count(*) FROM items WHERE id = $1 AND list_id = $2 AND sort_order = 4`, it, target) != 1 {
		t.Errorf("item root after restore: version %d deleted %v batch %v", version, deleted, got)
	}
}

func TestRepoPurgeReads(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	s := e.folder("S", &f)
	l1 := e.list("L1", &f)
	l2 := e.list("L2", &s)
	i1 := e.item(l1, "I1")
	i2 := e.item(l2, "I2")
	e.item(l2, "I3")
	trashedList := e.list("L3", &f)
	e.trash("lists", trashedList, e.batch(trash.TypeList, trashedList, "L3", now, `[]`), now)
	e.trash("items", i1, e.batch(trash.TypeItem, i1, "I1", now, `[]`), now)
	var att uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO attachments (workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, 'images/aa/bb/k.png', 'images/aa/bb/k.thumb.png', 'image/png', 10, 1, 1, $2) RETURNING id`, e.ws, make([]byte, 32)).Scan(&att); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE items SET image_attachment_id = $2 WHERE id = $1`, i2, att)

	// Purge reads cover every state: the trashed list and item are included.
	var lists, items []uuid.UUID
	var files []trash.AttachmentFile
	e.must(func(r trash.Repo) (err error) {
		lists, err = r.ListListIDsByFolders(e.ctx, []uuid.UUID{f, s})
		return err
	})
	if len(lists) != 3 || !slices.Contains(lists, trashedList) {
		t.Errorf("lists under F and S = %v, want L1, L2 and the trashed L3", lists)
	}
	e.must(func(r trash.Repo) (err error) {
		items, err = r.ListItemIDsByLists(e.ctx, []uuid.UUID{l1, l2})
		return err
	})
	if len(items) != 3 || !slices.Contains(items, i1) {
		t.Errorf("items of L1 and L2 = %v, want all three", items)
	}
	e.must(func(r trash.Repo) (err error) {
		files, err = r.ListAttachmentFiles(e.ctx, []uuid.UUID{i1, i2})
		return err
	})
	if len(files) != 1 || files[0].ID != att || files[0].StorageKey != "images/aa/bb/k.png" || files[0].ThumbnailStorageKey != "images/aa/bb/k.thumb.png" {
		t.Errorf("attachment files = %+v", files)
	}
}

func TestRepoPurgeDeletes(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	l := e.list("L", &f)
	it := e.item(l, "I")
	var att uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `INSERT INTO attachments (workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, 'images/cc/dd/p.png', 'images/cc/dd/p.thumb.png', 'image/png', 10, 1, 1, $2) RETURNING id`, e.ws, make([]byte, 32)).Scan(&att); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE items SET image_attachment_id = $2 WHERE id = $1`, it, att)
	e.exec(`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, 1, 'first', 5, $2)`, it, make([]byte, 32))
	e.exec(`INSERT INTO lookup_imports (workspace_id, item_id, type, original_title, fetched_at, provider, source_id)
		VALUES ($1, $2, 'book', 'I', now(), 'openlibrary', 'OL27448W')`, e.ws, it)

	// Children first, as the purge deletes them: content, lookups, items, lists, folders,
	// and the attachment rows once the items no longer point to them.
	e.must(func(r trash.Repo) error { return r.DeleteContentVersions(e.ctx, []uuid.UUID{it}) })
	e.must(func(r trash.Repo) error { return r.DeleteLookupImports(e.ctx, []uuid.UUID{it}) })
	if n := e.count(`SELECT count(*) FROM item_content_versions WHERE item_id = $1`, it); n != 0 {
		t.Errorf("content versions = %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM lookup_imports WHERE item_id = $1`, it); n != 0 {
		t.Errorf("lookup imports = %d, want 0", n)
	}
	e.must(func(r trash.Repo) error { return r.DeleteItems(e.ctx, []uuid.UUID{it}) })
	e.must(func(r trash.Repo) error { return r.DeleteAttachmentRows(e.ctx, []uuid.UUID{att}) })
	e.must(func(r trash.Repo) error { return r.DeleteLists(e.ctx, []uuid.UUID{l}) })
	e.must(func(r trash.Repo) error { return r.DeleteFolders(e.ctx, []uuid.UUID{f}) })
	for _, table := range []string{"items", "attachments", "lists", "folders"} {
		if n := e.count("SELECT count(*) FROM "+table+" WHERE workspace_id = $1", e.ws); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

func TestRepoIsWorkspaceScoped(t *testing.T) {
	e := newEnv(t)
	_, otherCtx := e.otherWorkspace()
	var foreignFolder, foreignBatch uuid.UUID
	if err := e.pool.QueryRow(otherCtx, `INSERT INTO folders (workspace_id, name, sort_order) SELECT id, 'X', 0 FROM workspaces WHERE name = 'Other' RETURNING id`).Scan(&foreignFolder); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(otherCtx, `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path, deleted_at)
		SELECT id, 'folder', $1, 'X', '[]', $2 FROM workspaces WHERE name = 'Other' RETURNING id`, foreignFolder, now).Scan(&foreignBatch); err != nil {
		t.Fatal(err)
	}

	e.must(func(r trash.Repo) error {
		nodes, err := r.FolderParents(e.ctx)
		if err != nil || len(nodes) != 0 {
			t.Errorf("FolderParents = %+v, %v; want none of the other workspace", nodes, err)
		}
		if _, err := r.GetActiveFolder(e.ctx, foreignFolder); !isNotFound(err) {
			t.Errorf("GetActiveFolder of another workspace err = %v, want NotFound", err)
		}
		if _, err := r.LockFolder(e.ctx, foreignFolder); !isNotFound(err) {
			t.Errorf("LockFolder of another workspace err = %v, want NotFound", err)
		}
		ids, err := r.BatchIDs(e.ctx)
		if err != nil || len(ids) != 0 {
			t.Errorf("BatchIDs = %v, %v; want none of the other workspace", ids, err)
		}
		if _, err := r.LockBatch(e.ctx, foreignBatch); !isNotFound(err) {
			t.Errorf("LockBatch of another workspace err = %v, want NotFound", err)
		}
		return nil
	})
	if n := e.count(`SELECT count(*) FROM trash_batches WHERE id = $1`, foreignBatch); n != 1 {
		t.Errorf("foreign batch rows = %d, want 1", n)
	}
}
