package access_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/access"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

var (
	alice = uuid.MustParse("00000000-0000-4000-8000-00000000a11c")
	bob   = uuid.MustParse("00000000-0000-4000-8000-0000000000b0")
)

func id(n byte) uuid.UUID { return uuid.UUID{15: n} }

func ptr[T any](v T) *T { return &v }

// world: alice owns top folder 1 (nested folder 2 inside, trashed folder 3 inside that) and top list 10;
// list 11 is in folder 2; bob owns top folder 4 and list 12 in it; folder 5 is a top-level orphan nobody owns.
func world() access.Snapshot {
	return access.NewSnapshot(
		[]access.Node{
			{ID: id(1), Owner: &alice},
			{ID: id(2), ParentID: ptr(id(1))},
			{ID: id(3), ParentID: ptr(id(2)), Trashed: true},
			{ID: id(4), Owner: &bob},
			{ID: id(5)},
		},
		[]access.Node{
			{ID: id(10), Owner: &alice},
			{ID: id(11), ParentID: ptr(id(2))},
			{ID: id(12), ParentID: ptr(id(4))},
			{ID: id(13), ParentID: ptr(id(3)), Trashed: true},
		},
	)
}

func TestSnapshot_BoxAndVisible(t *testing.T) {
	s := world()
	tests := []struct {
		name  string
		box   access.Box
		alice bool
		bob   bool
	}{
		{"top folder", s.FolderBox(id(1)), true, false},
		{"nested folder inherits", s.FolderBox(id(2)), true, false},
		{"trashed folder keeps its box", s.FolderBox(id(3)), true, false},
		{"top list", s.ListBox(id(10)), true, false},
		{"list in a nested folder", s.ListBox(id(11)), true, false},
		{"trashed list in a trashed folder", s.ListBox(id(13)), true, false},
		{"other user's folder", s.FolderBox(id(4)), false, true},
		{"other user's list", s.ListBox(id(12)), false, true},
		{"top folder nobody owns", s.FolderBox(id(5)), false, false},
		{"missing folder", s.FolderBox(id(99)), false, false},
		{"missing list", s.ListBox(id(99)), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Visible(alice, tt.box); got != tt.alice {
				t.Errorf("visible to alice = %v, want %v", got, tt.alice)
			}
			if got := s.Visible(bob, tt.box); got != tt.bob {
				t.Errorf("visible to bob = %v, want %v", got, tt.bob)
			}
			if s.Visible(uuid.Nil, tt.box) {
				t.Error("visible to nobody-at-all (nil user)")
			}
		})
	}
}

func TestSnapshot_VisibleSetsAreActiveOnly(t *testing.T) {
	s := world()
	folders, lists := s.VisibleFolders(alice), s.VisibleLists(alice)
	if len(folders) != 2 || !folders[id(1)] || !folders[id(2)] {
		t.Errorf("alice's active folders = %v, want 1 and 2", folders)
	}
	if len(lists) != 2 || !lists[id(10)] || !lists[id(11)] {
		t.Errorf("alice's active lists = %v, want 10 and 11", lists)
	}
	if got := s.VisibleFolders(bob); len(got) != 1 || !got[id(4)] {
		t.Errorf("bob's folders = %v", got)
	}
}

func TestSnapshot_ParentCycleTerminates(t *testing.T) {
	s := access.NewSnapshot([]access.Node{
		{ID: id(1), ParentID: ptr(id(2))}, {ID: id(2), ParentID: ptr(id(1))},
	}, nil)
	if box := s.FolderBox(id(1)); box.Found {
		t.Errorf("a cycle resolved to a box: %+v", box)
	}
}

type fakeRepo struct {
	snap        access.Snapshot
	snapshots   int
	itemList    map[uuid.UUID]uuid.UUID
	attachItem  map[uuid.UUID]uuid.UUID
	roots       map[uuid.UUID]access.TrashRoot
	foldersTop  map[uuid.UUID]int
	listsTop    map[uuid.UUID]int
	topOrderFor uuid.UUID
}

func (f *fakeRepo) Snapshot(context.Context) (access.Snapshot, error) {
	f.snapshots++
	return f.snap, nil
}

func (f *fakeRepo) ItemList(_ context.Context, item uuid.UUID) (uuid.UUID, bool, error) {
	l, ok := f.itemList[item]
	return l, ok, nil
}

func (f *fakeRepo) AttachmentItem(_ context.Context, att uuid.UUID) (uuid.UUID, bool, error) {
	i, ok := f.attachItem[att]
	return i, ok, nil
}

func (f *fakeRepo) TrashRoot(_ context.Context, batch uuid.UUID) (access.TrashRoot, bool, error) {
	r, ok := f.roots[batch]
	return r, ok, nil
}

func (f *fakeRepo) TrashRoots(context.Context) ([]access.TrashRoot, error) {
	var out []access.TrashRoot
	for _, r := range f.roots {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeRepo) TopOrder(_ context.Context, user uuid.UUID) (map[uuid.UUID]int, map[uuid.UUID]int, error) {
	f.topOrderFor = user
	return f.foldersTop, f.listsTop, nil
}

func newFake() *fakeRepo {
	return &fakeRepo{
		snap:       world(),
		itemList:   map[uuid.UUID]uuid.UUID{id(20): id(10), id(21): id(12), id(22): id(13)},
		attachItem: map[uuid.UUID]uuid.UUID{id(30): id(20), id(31): id(21)},
		roots: map[uuid.UUID]access.TrashRoot{
			id(40): {BatchID: id(40), Type: "folder", RootID: id(3)},
			id(41): {BatchID: id(41), Type: "list", RootID: id(12)},
			id(42): {BatchID: id(42), Type: "item", RootID: id(22), ItemList: ptr(id(13))},
			id(43): {BatchID: id(43), Type: "item", RootID: id(99)}, // the item row is gone
		},
	}
}

func TestService_VisibleByKind(t *testing.T) {
	svc := access.NewService(newFake())
	ctx := scope.WithUser(context.Background(), alice)
	tests := []struct {
		kind access.Kind
		id   uuid.UUID
		want bool
	}{
		{access.KindFolder, id(2), true},
		{access.KindFolder, id(4), false},
		{access.KindList, id(11), true},
		{access.KindList, id(12), false},
		{access.KindItem, id(20), true},
		{access.KindItem, id(21), false},
		{access.KindItem, id(22), true}, // in a trashed list: any state
		{access.KindItem, id(98), false},
		{access.KindAttachment, id(30), true},
		{access.KindAttachment, id(31), false},
		{access.KindAttachment, id(97), false}, // an orphan belongs to nobody
		{access.KindTrashBatch, id(40), true},
		{access.KindTrashBatch, id(41), false},
		{access.KindTrashBatch, id(42), true},
		{access.KindTrashBatch, id(43), false},
		{access.KindTrashBatch, id(96), false},
		{access.Kind("bogus"), id(1), false},
	}
	for _, tt := range tests {
		got, err := svc.Visible(ctx, tt.kind, tt.id)
		if err != nil || got != tt.want {
			t.Errorf("Visible(%s, %v) = %v, %v; want %v", tt.kind, tt.id, got, err, tt.want)
		}
	}
}

func TestService_NoUserSeesNothing(t *testing.T) {
	svc := access.NewService(newFake())
	ctx := context.Background()
	if ok, err := svc.FolderVisible(ctx, id(1)); err != nil || ok {
		t.Errorf("FolderVisible without a user = %v, %v", ok, err)
	}
	if f, _ := svc.VisibleFolderIDs(ctx); len(f) != 0 {
		t.Errorf("folders without a user = %v", f)
	}
	if l, _ := svc.VisibleListIDs(ctx); len(l) != 0 {
		t.Errorf("lists without a user = %v", l)
	}
	if b, err := svc.VisibleBatchIDs(ctx); err != nil || b == nil || len(b) != 0 {
		t.Errorf("batches without a user = %v, %v; want an empty non-nil slice", b, err)
	}
}

func TestService_VisibleBatchIDs(t *testing.T) {
	svc := access.NewService(newFake())
	got, err := svc.VisibleBatchIDs(scope.WithUser(context.Background(), alice))
	if err != nil {
		t.Fatal(err)
	}
	set := map[uuid.UUID]bool{}
	for _, b := range got {
		set[b] = true
	}
	if len(set) != 2 || !set[id(40)] || !set[id(42)] {
		t.Errorf("alice's batches = %v, want 40 and 42", got)
	}
}

func TestService_CacheReadsTheSnapshotOncePerRequest(t *testing.T) {
	repo := newFake()
	svc := access.NewService(repo)
	ctx := access.WithCache(scope.WithUser(context.Background(), alice))
	for range 3 {
		if _, err := svc.FolderVisible(ctx, id(1)); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.VisibleListIDs(access.WithCache(ctx)); err != nil { // idempotent
			t.Fatal(err)
		}
	}
	if repo.snapshots != 1 {
		t.Errorf("snapshot read %d times in one request, want 1", repo.snapshots)
	}
	// Without a cache every question reads it again.
	plain := scope.WithUser(context.Background(), alice)
	_, _ = svc.FolderVisible(plain, id(1))
	_, _ = svc.FolderVisible(plain, id(1))
	if repo.snapshots != 3 {
		t.Errorf("uncached reads = %d, want 2 more", repo.snapshots-1)
	}
}

func TestService_TopOrderIsTheCallers(t *testing.T) {
	repo := newFake()
	repo.foldersTop = map[uuid.UUID]int{id(1): 0}
	svc := access.NewService(repo)
	f, _, err := svc.TopOrder(scope.WithUser(context.Background(), bob))
	if err != nil || f[id(1)] != 0 || repo.topOrderFor != bob {
		t.Errorf("TopOrder = %v, %v for %s", f, err, repo.topOrderFor)
	}
}
