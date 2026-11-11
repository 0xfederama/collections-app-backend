package access_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/access"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
)

func TestMain(m *testing.M) {
	m.Run()
	testdb.Terminate()
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type backfillEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	ctx  context.Context
	ws   uuid.UUID
}

func newBackfillEnv(t *testing.T) *backfillEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, ws); err != nil {
		t.Fatal(err)
	}
	return &backfillEnv{t: t, pool: pool, ctx: scope.With(t.Context(), ws), ws: ws}
}

func (e *backfillEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *backfillEnv) folder(name string, parent any, trashed bool) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	if trashed {
		batch := uuid.New()
		e.exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, $2, 'folder', $3, 'x', '[]')`, batch, e.ws, id)
		e.exec(`INSERT INTO folders (id, workspace_id, parent_folder_id, name, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, $4, 0, now(), $5)`, id, e.ws, parent, name, batch)
		return id
	}
	e.exec(`INSERT INTO folders (id, workspace_id, parent_folder_id, name, sort_order) VALUES ($1, $2, $3, $4, 0)`, id, e.ws, parent, name)
	return id
}

func (e *backfillEnv) list(name string, folder any, trashed bool) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	if trashed {
		batch := uuid.New()
		e.exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, $2, 'list', $3, 'x', '[]')`, batch, e.ws, id)
		e.exec(`INSERT INTO lists (id, workspace_id, folder_id, name, sort_order, deleted_at, trash_batch_id) VALUES ($1, $2, $3, $4, 0, now(), $5)`, id, e.ws, folder, name, batch)
		return id
	}
	e.exec(`INSERT INTO lists (id, workspace_id, folder_id, name, sort_order) VALUES ($1, $2, $3, $4, 0)`, id, e.ws, folder, name)
	return id
}

func (e *backfillEnv) owner(table string, id uuid.UUID) *uuid.UUID {
	e.t.Helper()
	var o *uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `SELECT owner_user_id FROM `+table+` WHERE id = $1`, id).Scan(&o); err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *backfillEnv) userCount() (n int) {
	e.t.Helper()
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestBackfill_AdoptsEveryOwnerlessTopLevelRowOfAnyState(t *testing.T) {
	e := newBackfillEnv(t)
	top := e.folder("top", nil, false)
	nested := e.folder("nested", top, false)
	trashedTop := e.folder("trashed top", nil, true)
	trashedNested := e.folder("trashed nested", top, true)
	topList := e.list("top list", nil, false)
	trashedList := e.list("trashed list", nil, true)
	innerList := e.list("inner", nested, false)

	pg := access.NewPostgres(e.pool)
	if err := access.Backfill(e.ctx, pg, "  Owner@Example.COM ", quiet()); err != nil {
		t.Fatal(err)
	}

	var owner uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `SELECT id FROM users WHERE email = 'owner@example.com'`).Scan(&owner); err != nil {
		t.Fatalf("the owner user (lower-cased e-mail) was not created: %v", err)
	}
	for name, got := range map[string]*uuid.UUID{
		"top folder": e.owner("folders", top), "trashed top folder": e.owner("folders", trashedTop),
		"top list": e.owner("lists", topList), "trashed top list": e.owner("lists", trashedList),
	} {
		if got == nil || *got != owner {
			t.Errorf("%s owner = %v, want %s", name, got, owner)
		}
	}
	for name, got := range map[string]*uuid.UUID{
		"nested folder": e.owner("folders", nested), "trashed nested folder": e.owner("folders", trashedNested),
		"list in a folder": e.owner("lists", innerList),
	} {
		if got != nil {
			t.Errorf("%s owner = %v, want null", name, *got)
		}
	}

	// A second run (the next startup) changes nothing and needs no e-mail.
	if err := access.Backfill(e.ctx, pg, "", quiet()); err != nil {
		t.Fatalf("second run without OWNER_EMAIL: %v", err)
	}
	if n := e.userCount(); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestBackfill_KeepsExistingOwnersAndAdoptsOnlyTheOwnerless(t *testing.T) {
	e := newBackfillEnv(t)
	other := uuid.New()
	e.exec(`INSERT INTO users (id, workspace_id, email) VALUES ($1, $2, 'someone@example.com')`, other, e.ws)
	owned := e.folder("owned", nil, false)
	e.exec(`UPDATE folders SET owner_user_id = $1 WHERE id = $2`, other, owned)
	orphan := e.folder("orphan", nil, false)

	if err := access.Backfill(e.ctx, access.NewPostgres(e.pool), "owner@example.com", quiet()); err != nil {
		t.Fatal(err)
	}
	if got := e.owner("folders", owned); got == nil || *got != other {
		t.Errorf("an existing owner was replaced: %v", got)
	}
	if got := e.owner("folders", orphan); got == nil || *got == other {
		t.Errorf("the orphan was not given to the configured owner: %v", got)
	}
}

func TestBackfill_NeedsTheOwnerEmailOnlyWhenThereIsSomethingToAdopt(t *testing.T) {
	t.Run("an empty database needs nothing", func(t *testing.T) {
		e := newBackfillEnv(t)
		if err := access.Backfill(e.ctx, access.NewPostgres(e.pool), "", quiet()); err != nil {
			t.Fatalf("fresh install: %v", err)
		}
		if n := e.userCount(); n != 0 {
			t.Errorf("a no-op created %d users", n)
		}
	})
	t.Run("ownerless rows without an e-mail stop the startup", func(t *testing.T) {
		e := newBackfillEnv(t)
		top := e.folder("top", nil, false)
		err := access.Backfill(e.ctx, access.NewPostgres(e.pool), "   ", quiet())
		if !errors.Is(err, access.ErrOwnerEmailRequired) || !strings.Contains(err.Error(), "OWNER_EMAIL") {
			t.Fatalf("err = %v, want ErrOwnerEmailRequired naming OWNER_EMAIL", err)
		}
		if got := e.owner("folders", top); got != nil {
			t.Errorf("a refused backfill still changed data: %v", got)
		}
		if n := e.userCount(); n != 0 {
			t.Errorf("a refused backfill created %d users", n)
		}
	})
	t.Run("only nested rows are nothing to adopt", func(t *testing.T) {
		e := newBackfillEnv(t)
		owner := uuid.New()
		e.exec(`INSERT INTO users (id, workspace_id, email) VALUES ($1, $2, 'o@example.com')`, owner, e.ws)
		top := e.folder("top", nil, false)
		e.exec(`UPDATE folders SET owner_user_id = $1 WHERE id = $2`, owner, top)
		e.list("inner", top, false)
		if err := access.Backfill(e.ctx, access.NewPostgres(e.pool), "", quiet()); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBackfill_ConcurrentStartupsAgree(t *testing.T) {
	e := newBackfillEnv(t)
	top := e.folder("top", nil, false)
	pg := access.NewPostgres(e.pool)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = access.Backfill(e.ctx, pg, "owner@example.com", quiet()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
	if n := e.userCount(); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
	if got := e.owner("folders", top); got == nil {
		t.Error("the folder was not adopted")
	}
}
