package trash_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// rowCounts is how many rows of each kind the fake holds: the unscoped tests
// assert that none of them changed.
type rowCounts struct{ folders, lists, items, batches int }

func countRows(db *fakeDB) rowCounts {
	return rowCounts{len(db.data.folders), len(db.data.lists), len(db.data.items), len(db.data.batches)}
}

// TestUnscopedContextRefusesEveryTrashOperation: a context with no workspace is
// refused before any read or write, for every entry point that deletes or purges.
func TestUnscopedContextRefusesEveryTrashOperation(t *testing.T) {
	type call func() error
	cases := []struct {
		name string
		// setup builds the rows the call would act on and returns the call.
		setup func(r rig) call
	}{
		{"DeleteFolder", func(r rig) call {
			f := r.db.folder("F", uuid.Nil, 0)
			l := r.db.list("L", f, 0)
			r.db.item("I", l, 0)
			return func() error { return r.svc.DeleteFolder(context.Background(), f, r.db.data.folders[f].version) }
		}},
		{"DeleteList", func(r rig) call {
			l := r.db.list("L", uuid.Nil, 0)
			r.db.item("I", l, 0)
			return func() error { return r.svc.DeleteList(context.Background(), l, r.db.data.lists[l].version) }
		}},
		{"DeleteItem", func(r rig) call {
			l := r.db.list("L", uuid.Nil, 0)
			i := r.db.item("I", l, 0)
			return func() error { return r.svc.DeleteItem(context.Background(), i, r.db.data.items[i].version) }
		}},
		{"Restore", func(r rig) call {
			l := r.db.list("L", uuid.Nil, 0)
			_, b := trashedItem(r.db, "I", l, r.now.Add(-time.Hour))
			return func() error { _, err := r.svc.Restore(context.Background(), b, nil); return err }
		}},
		{"Purge", func(r rig) call {
			l := r.db.list("L", uuid.Nil, 0)
			_, b := trashedItem(r.db, "I", l, r.now.Add(-time.Hour))
			return func() error { return r.svc.Purge(context.Background(), b) }
		}},
		{"PurgeAll", func(r rig) call {
			l := r.db.list("L", uuid.Nil, 0)
			trashedItem(r.db, "I", l, r.now.Add(-time.Hour))
			return func() error { _, err := r.svc.PurgeAll(context.Background()); return err }
		}},
		{"PurgeExpired", func(r rig) call {
			r.db.data.retention = new(int)
			l := r.db.list("L", uuid.Nil, 0)
			trashedItem(r.db, "I", l, r.now.Add(-time.Hour))
			return func() error { _, err := r.svc.PurgeExpired(context.Background()); return err }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			run := tc.setup(r)
			before := countRows(r.db)
			if err := run(); err == nil {
				t.Fatal("call with an unscoped context returned no error")
			} else if !strings.Contains(err.Error(), "workspace") {
				t.Errorf("error = %v, want one naming the missing workspace", err)
			}
			if len(r.db.calls) != 0 {
				t.Errorf("calls = %v, want none (no transaction for an unscoped context)", r.db.calls)
			}
			if after := countRows(r.db); after != before {
				t.Errorf("rows = %+v, want %+v unchanged", after, before)
			}
		})
	}
}

// RunAutoPurge logs the refusal at ERROR on every run and deletes nothing. The
// context is cancelled up front, so the single run before the first tick is the
// only one, as in the sweeper test.
func TestRunAutoPurgeLogsUnscopedContextAndDeletesNothing(t *testing.T) {
	r := newRig(t, false)
	r.db.data.retention = new(int)
	l := r.db.list("L", uuid.Nil, 0)
	trashedItem(r.db, "old", l, r.now.Add(-time.Hour*24*2))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.svc.RunAutoPurge(ctx, time.Hour)

	if !strings.Contains(r.logs.String(), "level=ERROR") {
		t.Errorf("no ERROR logged for an unscoped auto-purge run: %q", r.logs.String())
	}
	if len(r.db.data.batches) != 1 {
		t.Errorf("batches left = %d, want the expired one kept", len(r.db.data.batches))
	}
	if len(r.db.calls) != 0 {
		t.Errorf("calls = %v, want none", r.db.calls)
	}
}
