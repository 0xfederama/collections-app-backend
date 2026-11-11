package trash_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// withFile gives item id an image attachment with two stored files.
func withFile(t *testing.T, r rig, item uuid.UUID, base string) (string, string) {
	t.Helper()
	f, th := "images/aa/bb/"+base+".jpg", "images/aa/bb/"+base+"_t.jpg"
	for _, k := range []string{f, th} {
		if _, err := r.local.Put(k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	it := r.db.data.items[item]
	it.file = &trash.AttachmentFile{ID: uuid.New(), StorageKey: f, ThumbnailStorageKey: th}
	r.db.data.items[item] = it
	return f, th
}

// fileExists reports whether key is still in the file store.
func fileExists(t *testing.T, r rig, key string) bool {
	t.Helper()
	rc, _, err := r.local.Open(key)
	if errors.Is(err, storage.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	return true
}

// purgeFixture trashes folder A (in batch ba) with subfolder B, a list in each and
// one attached item in each list; it also leaves an unrelated active folder X
// with a list and an item. It returns the ids the tests need.
type purgeFixture struct {
	a, b, ba, i1, i2 uuid.UUID
	f1, t1, f2, t2   string
}

func newPurgeFixture(t *testing.T, r rig) purgeFixture {
	t.Helper()
	db := r.db
	x := db.folder("X", uuid.Nil, 0)
	xl := db.list("XL", x, 0)
	db.item("XI", xl, 0)

	a, ba := trashedFolder(db, "A", uuid.Nil, r.now.Add(-time.Hour))
	b := uuid.New()
	db.putFolder(b, "B", a, 0, ba)
	l := uuid.New()
	db.putList(l, "L", a, 0, ba)
	l2 := uuid.New()
	db.putList(l2, "L2", b, 0, ba)
	i1 := uuid.New()
	db.putItem(i1, "I1", l, 0, ba)
	i2 := uuid.New()
	db.putItem(i2, "I2", l2, 0, ba)
	f := purgeFixture{a: a, b: b, ba: ba, i1: i1, i2: i2}
	f.f1, f.t1 = withFile(t, r, i1, "f1")
	f.f2, f.t2 = withFile(t, r, i2, "f2")
	return f
}

func TestPurgeFolderOrderAndFilesAfterCommit(t *testing.T) {
	r := newRig(t, false)
	fx := newPurgeFixture(t, r)
	before := r.db.batchByName()

	if err := r.svc.Purge(r.ctx, fx.ba); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	want := []string{
		"LockTree", "LockBatch",
		"DeleteContentVersions", "DeleteLookupImports", "DeleteItems", "DeleteLists", "DeleteFolderMembers", "DeleteFolders", "DeleteTopOrder", "DeleteAttachmentRows",
		"DeleteBatch", "DeleteOrphanBatches",
		"commit",
		"delete:" + fx.f1, "delete:" + fx.t1, "delete:" + fx.f2, "delete:" + fx.t2,
	}
	if !slices.Equal(r.db.calls, want) {
		t.Fatalf("calls = %v\nwant %v", r.db.calls, want)
	}
	for _, k := range []string{fx.f1, fx.t1, fx.f2, fx.t2} {
		if fileExists(t, r, k) {
			t.Errorf("file %s is still there", k)
		}
	}
	got := r.db.batchByName()
	for name := range before {
		if name == "A" || name == "B" || name == "L" || name == "L2" || name == "I1" || name == "I2" {
			if _, ok := got[name]; ok {
				t.Errorf("%s was not purged", name)
			}
		}
	}
	if _, ok := got["X"]; !ok || got["X"] != uuid.Nil || got["XI"] != uuid.Nil {
		t.Errorf("the unrelated active rows changed: %v", got)
	}
	if len(r.db.data.batches) != 0 {
		t.Errorf("batches left: %d", len(r.db.data.batches))
	}
}

func TestPurgeDropsNestedBatchesWhoseRootWentAway(t *testing.T) {
	r := newRig(t, false)
	db := r.db
	a, ba := trashedFolder(db, "A", uuid.Nil, r.now.Add(-time.Hour))
	// B was deleted earlier on its own: it has its own batch, nested in A's subtree.
	b, bb := trashedFolder(db, "B", a, r.now.Add(-time.Minute))
	_ = b

	if err := r.svc.Purge(r.ctx, ba); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(db.data.batches) != 0 {
		t.Errorf("batches left = %d, want 0 (the nested batch %v must go too)", len(db.data.batches), bb)
	}
	if len(db.data.folders) != 0 {
		t.Errorf("folders left = %d, want 0", len(db.data.folders))
	}
}

func TestPurgeFailureRollsBackAndKeepsFiles(t *testing.T) {
	r := newRig(t, false)
	fx := newPurgeFixture(t, r)
	before := r.db.batchByName()
	r.db.failOn = "DeleteLists"

	if err := r.svc.Purge(r.ctx, fx.ba); err == nil {
		t.Fatal("Purge succeeded, want the injected failure")
	}
	if last := r.db.calls[len(r.db.calls)-1]; last != "rollback" {
		t.Errorf("last call = %q, want rollback", last)
	}
	for _, c := range r.db.calls {
		if c == "commit" || strings.HasPrefix(c, "delete:") {
			t.Errorf("call %q after a failed transaction", c)
		}
	}
	if got := r.db.batchByName(); !maps.Equal(got, before) {
		t.Errorf("rows changed on rollback: %v", got)
	}
	for _, k := range []string{fx.f1, fx.t1, fx.f2, fx.t2} {
		if !fileExists(t, r, k) {
			t.Errorf("file %s was deleted by a failed purge", k)
		}
	}
}

func TestPurgeFileFailureIsLoggedNotReturned(t *testing.T) {
	r := newRig(t, true)
	fx := newPurgeFixture(t, r)

	if err := r.svc.Purge(r.ctx, fx.ba); err != nil {
		t.Fatalf("Purge: %v, want nil (a file failure is only logged)", err)
	}
	if !strings.Contains(r.logs.String(), "image file cleanup after purge failed") {
		t.Errorf("no log for the failed file delete: %q", r.logs.String())
	}
	if got := r.db.batchByName(); len(got) != 3 || got["X"] != uuid.Nil || got["XL"] != uuid.Nil || got["XI"] != uuid.Nil {
		// Only the unrelated X, XL and XI remain.
		t.Errorf("rows left = %v, want only X, XL and XI, all active", got)
	}
}

func TestPurgeAllRemovesTheWholeTrashInOneTransaction(t *testing.T) {
	r := newRig(t, false)
	db := r.db
	fx := newPurgeFixture(t, r)
	// A folder N trashed on its own inside A's subtree: its batch goes with A's purge.
	trashedFolder(db, "N", fx.a, r.now.Add(-time.Minute))
	// A list and an item, each trashed in its own batch (their parents stay active).
	f := db.folder("F", uuid.Nil, 0)
	trashedList(db, "LT", f, r.now)
	l := db.list("LX", f, 0)
	trashedItem(db, "IX", l, r.now)

	removed, err := r.svc.PurgeAll(r.ctx)
	if err != nil {
		t.Fatalf("PurgeAll: %v", err)
	}
	// A's batch, N's batch (gone with A, skipped), the list batch and the item batch.
	if removed != 4 {
		t.Errorf("removed = %d, want 4", removed)
	}
	if n := countOf(r.db.calls, "commit"); n != 1 {
		t.Errorf("commits = %d, want 1", n)
	}
	if n := countOf(r.db.calls, "LockTree"); n != 1 {
		t.Errorf("tree locks = %d, want 1", n)
	}
	if len(db.data.batches) != 0 {
		t.Errorf("batches left = %d", len(db.data.batches))
	}
	for _, id := range []uuid.UUID{fx.a, fx.b} {
		if _, ok := db.data.folders[id]; ok {
			t.Errorf("folder %v still there", id)
		}
	}
	for _, k := range []string{fx.f1, fx.t1, fx.f2, fx.t2} {
		if fileExists(t, r, k) {
			t.Errorf("file %s is still there", k)
		}
	}
	if last := r.db.calls[len(r.db.calls)-1]; !strings.HasPrefix(last, "delete:") {
		t.Errorf("last call = %q, want the file deletes after the commit", last)
	}
	if got := db.batchByName(); got["X"] != uuid.Nil || got["XI"] != uuid.Nil {
		t.Errorf("the active X rows changed: %v", got)
	}
}

func countOf(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

func TestPurgeAllEmptyTrash(t *testing.T) {
	r := newRig(t, false)
	removed, err := r.svc.PurgeAll(r.ctx)
	if err != nil || removed != 0 {
		t.Fatalf("PurgeAll = %d, %v; want 0, nil", removed, err)
	}
	if want := []string{"LockTree", "commit"}; !slices.Equal(r.db.calls, want) {
		t.Errorf("calls = %v, want %v", r.db.calls, want)
	}
}

func TestPurgeExpiredByRetention(t *testing.T) {
	intp := func(v int) *int { return &v }
	cases := []struct {
		name      string
		retention *int
		// ages of the trashed items, in days ago
		ages        []int
		wantRemoved int
		wantLeft    int
		wantNoTx    bool
	}{
		{"off purges nothing and opens no transaction", nil, []int{30, 8}, 0, 2, true},
		{"only batches older than the retention", intp(7), []int{8, 6}, 1, 1, false},
		{"zero days purges every batch trashed before now", intp(0), []int{6, 0}, 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			r.db.data.retention = tc.retention
			l := r.db.list("L", uuid.Nil, 0)
			for i, age := range tc.ages {
				at := r.now.Add(-time.Duration(age) * 24 * time.Hour)
				trashedItem(r.db, "I"+string(rune('a'+i)), l, at)
			}
			removed, err := r.svc.PurgeExpired(r.ctx)
			if err != nil {
				t.Fatalf("PurgeExpired: %v", err)
			}
			if removed != tc.wantRemoved {
				t.Errorf("removed = %d, want %d", removed, tc.wantRemoved)
			}
			if got := len(r.db.data.batches); got != tc.wantLeft {
				t.Errorf("batches left = %d, want %d", got, tc.wantLeft)
			}
			if tc.wantNoTx && len(r.db.calls) != 0 {
				t.Errorf("calls = %v, want none", r.db.calls)
			}
		})
	}
}

func TestRunAutoPurgeRunsOnceWhenCancelled(t *testing.T) {
	r := newRig(t, false)
	r.db.data.retention = new(int)
	l := r.db.list("L", uuid.Nil, 0)
	trashedItem(r.db, "old", l, r.now.Add(-time.Hour*24*2))

	ctx, cancel := context.WithCancel(r.ctx)
	cancel()
	r.svc.RunAutoPurge(ctx, time.Hour)

	if len(r.db.data.batches) != 0 {
		t.Errorf("batches left = %d, want the expired one purged on start-up", len(r.db.data.batches))
	}
	if !strings.Contains(r.logs.String(), "trash auto-purge finished") {
		t.Errorf("no finished log: %q", r.logs.String())
	}
}
