package lists

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// fakeDB is an in-memory Store and Repo. Every write is recorded in calls, so a
// test can assert what was written and in which order.
type fakeDB struct {
	lists   map[uuid.UUID]List
	folders map[uuid.UUID]fakeFolder
	types   map[string]fakeType
	calls   []string

	inTx           bool                     // a transaction is open (InTx)
	typeReadsInTx  int                      // TypeInfo calls made while inTx: must stay 0
	beforeLockHook func(*fakeDB, uuid.UUID) // runs once before GetForUpdate, to simulate a concurrent write
}

type fakeFolder struct {
	name   string
	parent *uuid.UUID
}

type fakeType struct {
	info   TypeInfo
	fields []FieldInfo
}

func newFakeDB() *fakeDB {
	return &fakeDB{lists: map[uuid.UUID]List{}, folders: map[uuid.UUID]fakeFolder{}, types: map[string]fakeType{
		// movie allows no price and has a default sort field; wishlist allows a price.
		"movie": {
			info:   TypeInfo{Key: "movie", DefaultSortField: ptr("release_year")},
			fields: []FieldInfo{{Key: "release_year", FieldType: "number"}, {Key: "director", FieldType: "text"}},
		},
		"wishlist": {info: TypeInfo{Key: "wishlist", AllowsPrice: true}},
	}}
}

func ptr[T any](v T) *T { return &v }

func (f *fakeDB) InTx(_ context.Context, fn func(Repo) error) error {
	f.inTx = true
	defer func() { f.inTx = false }()
	return fn(f)
}

func (f *fakeDB) LockTree(context.Context) error {
	f.calls = append(f.calls, "lock")
	return nil
}

func (f *fakeDB) Get(_ context.Context, id uuid.UUID) (List, error) {
	l, ok := f.lists[id]
	if !ok {
		return List{}, apperr.NotFound("list")
	}
	return l, nil
}

func (f *fakeDB) GetForUpdate(ctx context.Context, id uuid.UUID) (List, error) {
	if hook := f.beforeLockHook; hook != nil {
		f.beforeLockHook = nil
		hook(f, id)
	}
	return f.Get(ctx, id)
}

func (f *fakeDB) ListActive(context.Context) ([]List, error) {
	out := make([]List, 0, len(f.lists))
	for _, l := range f.lists {
		out = append(out, l)
	}
	return out, nil
}

func (f *fakeDB) ChildIDs(_ context.Context, folder *uuid.UUID) ([]uuid.UUID, error) {
	children := f.children(folder)
	ids := make([]uuid.UUID, 0, len(children))
	for _, l := range children {
		ids = append(ids, l.ID)
	}
	return ids, nil
}

func (f *fakeDB) children(folder *uuid.UUID) []List {
	var out []List
	for _, l := range f.lists {
		if (l.FolderID == nil) == (folder == nil) && (folder == nil || *l.FolderID == *folder) {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b List) int { return a.SortOrder - b.SortOrder })
	return out
}

func (f *fakeDB) NextSortOrder(_ context.Context, folder *uuid.UUID) (int, error) {
	return len(f.children(folder)), nil
}

func (f *fakeDB) Insert(_ context.Context, l List, now time.Time) (List, error) {
	f.calls = append(f.calls, "insert")
	l.ID = uuid.New()
	l.Version = 1
	l.CreatedAt, l.UpdatedAt = now, now
	f.lists[l.ID] = l
	return l, nil
}

func (f *fakeDB) Update(_ context.Context, l List, now time.Time) (List, error) {
	f.calls = append(f.calls, "update")
	cur := f.lists[l.ID]
	cur.Name, cur.Description, cur.DisplayMode, cur.SortMode = l.Name, l.Description, l.DisplayMode, l.SortMode
	cur.Icon, cur.IconColor = l.Icon, l.IconColor
	cur.Version++
	cur.UpdatedAt = now
	f.lists[l.ID] = cur
	return cur, nil
}

func (f *fakeDB) UpdateType(_ context.Context, id uuid.UUID, typ *string, now time.Time) (List, error) {
	f.calls = append(f.calls, "updateType")
	cur := f.lists[id]
	cur.Type, cur.Version, cur.UpdatedAt = typ, cur.Version+1, now
	f.lists[id] = cur
	return cur, nil
}

func (f *fakeDB) Move(_ context.Context, id uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) (List, error) {
	f.calls = append(f.calls, "move")
	cur := f.lists[id]
	cur.FolderID, cur.SortOrder, cur.Version, cur.UpdatedAt = folder, sortOrder, cur.Version+1, now
	f.lists[id] = cur
	return cur, nil
}

func (f *fakeDB) SetSortOrder(_ context.Context, id uuid.UUID, sortOrder int, now time.Time) error {
	f.calls = append(f.calls, "setSortOrder")
	cur := f.lists[id]
	cur.SortOrder, cur.UpdatedAt = sortOrder, now
	f.lists[id] = cur
	return nil
}

func (f *fakeDB) SetTopOrder(context.Context, uuid.UUID, int) error {
	f.calls = append(f.calls, "setTopOrder")
	return nil
}

func (f *fakeDB) FolderChain(_ context.Context, folderID uuid.UUID) ([]PathFolder, error) {
	chain := []PathFolder{}
	for id := &folderID; id != nil; {
		fo, ok := f.folders[*id]
		if !ok {
			break
		}
		chain = append([]PathFolder{{ID: *id, Name: fo.name}}, chain...)
		id = fo.parent
	}
	return chain, nil
}

func (f *fakeDB) FolderExists(_ context.Context, id uuid.UUID) (bool, error) {
	_, ok := f.folders[id]
	return ok, nil
}

func (f *fakeDB) TypeInfo(_ context.Context, key *string) (TypeInfo, []FieldInfo, error) {
	if f.inTx {
		f.typeReadsInTx++
	}
	if key == nil {
		return TypeInfo{}, nil, nil
	}
	t, ok := f.types[*key]
	if !ok {
		return TypeInfo{}, nil, apperr.NotFound("type")
	}
	return t.info, t.fields, nil
}

func (f *fakeDB) ClearItemsMetadata(context.Context, uuid.UUID, time.Time) error {
	f.calls = append(f.calls, "clearMetadata")
	return nil
}

func (f *fakeDB) ClearItemsPrice(context.Context, uuid.UUID, time.Time) error {
	f.calls = append(f.calls, "clearPrice")
	return nil
}

// writes reports whether any write (anything but the lock and the reads) ran.
func (f *fakeDB) writes() bool {
	for _, c := range f.calls {
		if c != "lock" {
			return true
		}
	}
	return false
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// seed adds an active list to the fake and returns it.
func (f *fakeDB) seed(l List) List {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	if l.Version == 0 {
		l.Version = 1
	}
	if l.DisplayMode == "" {
		l.DisplayMode = DefaultDisplayMode
	}
	if l.SortMode == "" {
		l.SortMode = DefaultSortMode
	}
	f.lists[l.ID] = l
	return l
}

// code returns the apperr conflict code of err, or "" when err is not a conflict.
func code(err error) apperr.Code {
	var c *apperr.ConflictError
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}

func TestCreate_Defaults(t *testing.T) {
	tests := []struct {
		name     string
		in       CreateInput
		wantName string
		wantType *string
		wantMode string
		wantSort string
	}{
		{"no type takes the built-in defaults", CreateInput{Name: "  Books  "}, "Books", nil, "thumbnail", "manual"},
		{"a type takes its default sort field", CreateInput{Name: "Films", Type: ptr("movie")}, "Films", ptr("movie"), "thumbnail", "release_year"},
		{"a type with no default sort field is manual", CreateInput{Name: "Wish", Type: ptr("wishlist")}, "Wish", ptr("wishlist"), "thumbnail", "manual"},
		{"an explicit sort wins over the default", CreateInput{Name: "F", Type: ptr("movie"), SortMode: "title"}, "F", ptr("movie"), "thumbnail", "title"},
		{"an explicit display mode is kept", CreateInput{Name: "G", DisplayMode: "grid"}, "G", nil, "grid", "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			got, err := NewService(db, db, fixedClock{}).Create(t.Context(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got.Name != tt.wantName || got.DisplayMode != tt.wantMode || got.SortMode != tt.wantSort || got.Version != 1 {
				t.Fatalf("got name=%q display=%q sort=%q version=%d", got.Name, got.DisplayMode, got.SortMode, got.Version)
			}
			if (got.Type == nil) != (tt.wantType == nil) || (got.Type != nil && *got.Type != *tt.wantType) {
				t.Fatalf("type = %v, want %v", got.Type, tt.wantType)
			}
			if got.Icon != nil || got.IconColor != nil {
				t.Fatalf("icon not defaulted: %+v", got)
			}
		})
	}
}

func TestCreate_DestinationUnavailableWritesNothing(t *testing.T) {
	db := newFakeDB()
	trashed := uuid.New() // not an active folder
	_, err := NewService(db, db, fixedClock{}).Create(t.Context(), CreateInput{Name: "L", FolderID: &trashed})
	if code(err) != apperr.CodeDestinationUnavailable {
		t.Fatalf("err = %v, want destination_unavailable", err)
	}
	if db.writes() {
		t.Fatalf("calls = %v, want no write", db.calls)
	}
}

func TestCreate_ValidationRunsBeforeTheTransaction(t *testing.T) {
	db := newFakeDB()
	_, err := NewService(db, db, fixedClock{}).Create(t.Context(), CreateInput{Name: "   "})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "name" {
		t.Fatalf("err = %v, want a validation error on name", err)
	}
	if len(db.calls) != 0 {
		t.Fatalf("calls = %v, want none", db.calls)
	}
}

func TestCreate_UnknownTypeIsValidationError(t *testing.T) {
	db := newFakeDB()
	_, err := NewService(db, db, fixedClock{}).Create(t.Context(), CreateInput{Name: "L", Type: ptr("nope")})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "type" {
		t.Fatalf("err = %v, want a validation error on type", err)
	}
	if db.writes() {
		t.Fatalf("calls = %v, want no write", db.calls)
	}
}

func TestUpdate_StaleVersionWritesNothing(t *testing.T) {
	db := newFakeDB()
	l := db.seed(List{Name: "A", Version: 3})
	_, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, 2, Patch{Name: ptr("B")})
	var vc *apperr.VersionConflictError
	if !errors.As(err, &vc) || vc.Current != 3 {
		t.Fatalf("err = %v, want version conflict at 3", err)
	}
	if db.writes() {
		t.Fatalf("calls = %v, want no write", db.calls)
	}
}

func TestUpdate_TypeChange(t *testing.T) {
	tests := []struct {
		name      string
		to        *string
		wantCalls []string
	}{
		// A type with a price keeps the items' prices: only metadata is cleared. The row is not
		// written again: no sort changes and the type update already bumped the version.
		{"to a type with a price clears metadata only", ptr("wishlist"),
			[]string{"lock", "updateType", "clearMetadata"}},
		// A type without a price clears the prices too. Its default sort field replaces the manual
		// sort, which is a row write of its own.
		{"to a type without a price clears metadata and price", ptr("movie"),
			[]string{"lock", "updateType", "clearMetadata", "clearPrice", "update"}},
		{"to no type clears metadata and price", nil,
			[]string{"lock", "updateType", "clearMetadata", "clearPrice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			from := ptr("wishlist")
			if tt.to != nil && *tt.to == "wishlist" {
				from = ptr("movie")
			}
			l := db.seed(List{Name: "A", Type: from, SortMode: "manual"})
			got, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, 1, Patch{Type: Optional[string]{Set: true, Value: tt.to}})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if !slices.Equal(db.calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", db.calls, tt.wantCalls)
			}
			if (got.Type == nil) != (tt.to == nil) || (got.Type != nil && *got.Type != *tt.to) {
				t.Fatalf("type = %v, want %v", got.Type, tt.to)
			}
		})
	}
}

func TestUpdate_NoOpPatchWritesNothing(t *testing.T) {
	db := newFakeDB()
	l := db.seed(List{Name: "A", Description: ptr("d"), Type: ptr("wishlist")})
	patch := Patch{
		Name:        ptr("A"),
		Description: Optional[string]{Set: true, Value: ptr("d")},
		DisplayMode: ptr("thumbnail"),
		Type:        Optional[string]{Set: true, Value: ptr("wishlist")}, // same type: lock only
	}
	got, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, 1, patch)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if db.writes() || got.Version != 1 {
		t.Fatalf("calls = %v, version = %d, want no write and version 1", db.calls, got.Version)
	}
}

func TestUpdate_UnknownTypeIsValidationError(t *testing.T) {
	db := newFakeDB()
	l := db.seed(List{Name: "A"})
	_, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, 1, Patch{Type: Optional[string]{Set: true, Value: ptr("nope")}})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "type" {
		t.Fatalf("err = %v, want a validation error on type", err)
	}
	if db.writes() {
		t.Fatalf("calls = %v, want no write", db.calls)
	}
}

// TestMove_ErrorOrder: the version is checked before the destination, and a missing
// or trashed destination is DestinationUnavailable. Neither writes anything.
func TestMove_ErrorOrder(t *testing.T) {
	tests := []struct {
		name    string
		version int
		wantVC  bool
	}{
		{"stale version is reported before the destination", 9, true},
		{"a destination that is not an active folder", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			l := db.seed(List{Name: "A"})
			gone := uuid.New()
			_, err := NewService(db, db, fixedClock{}).Move(t.Context(), l.ID, tt.version, &gone)
			var vc *apperr.VersionConflictError
			if tt.wantVC {
				if !errors.As(err, &vc) || vc.Current != 1 {
					t.Fatalf("err = %v, want version conflict at 1", err)
				}
			} else if code(err) != apperr.CodeDestinationUnavailable {
				t.Fatalf("err = %v, want destination_unavailable", err)
			}
			if db.writes() {
				t.Fatalf("calls = %v, want no write", db.calls)
			}
		})
	}
}

func TestMove_AppendsToTheDestination(t *testing.T) {
	db := newFakeDB()
	folder := uuid.New()
	db.folders[folder] = fakeFolder{name: "Media"}
	db.seed(List{Name: "Other", FolderID: &folder, SortOrder: 0})
	l := db.seed(List{Name: "A"})
	got, err := NewService(db, db, fixedClock{}).Move(t.Context(), l.ID, 1, &folder)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got.FolderID == nil || *got.FolderID != folder || got.SortOrder != 1 || got.Version != 2 {
		t.Fatalf("got folder=%v sort=%d version=%d, want the folder, sort 1, version 2", got.FolderID, got.SortOrder, got.Version)
	}
}

func TestReorder(t *testing.T) {
	db := newFakeDB()
	a := db.seed(List{Name: "A", SortOrder: 0})
	b := db.seed(List{Name: "B", SortOrder: 1})
	svc := NewService(db, db, fixedClock{})

	if err := svc.Reorder(t.Context(), nil, []uuid.UUID{a.ID}); code(err) != apperr.CodeOrderStale {
		t.Fatalf("missing sibling: err = %v, want order_stale", err)
	}
	missing := uuid.New()
	var nf *apperr.NotFoundError
	if err := svc.Reorder(t.Context(), &missing, nil); !errors.As(err, &nf) || nf.Resource != "folder" {
		t.Fatalf("missing folder: err = %v, want not found (folder)", err)
	}
	if err := svc.Reorder(t.Context(), nil, []uuid.UUID{b.ID, a.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if db.lists[b.ID].SortOrder != 0 || db.lists[a.ID].SortOrder != 1 {
		t.Fatalf("sort orders = %d, %d, want b=0 a=1", db.lists[b.ID].SortOrder, db.lists[a.ID].SortOrder)
	}
	if db.lists[a.ID].Version != 1 || db.lists[b.ID].Version != 1 {
		t.Fatal("reorder bumped a version")
	}
}

func TestGet_PathIsTheAncestorChain(t *testing.T) {
	db := newFakeDB()
	root := uuid.New()
	child := uuid.New()
	db.folders[root] = fakeFolder{name: "Media"}
	db.folders[child] = fakeFolder{name: "Movies", parent: &root}
	atRoot := db.seed(List{Name: "Loose"})
	inChild := db.seed(List{Name: "Watchlist", FolderID: &child})
	svc := NewService(db, db, fixedClock{})

	got, err := svc.Get(t.Context(), inChild.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Path) != 2 || got.Path[0].Name != "Media" || got.Path[1].Name != "Movies" {
		t.Fatalf("path = %+v, want Media then Movies", got.Path)
	}
	got, err = svc.Get(t.Context(), atRoot.ID)
	if err != nil || got.Path == nil || len(got.Path) != 0 {
		t.Fatalf("root list path = %#v, %v, want an empty non-nil path", got.Path, err)
	}
}

// TestTypeIsReadOutsideTheTransaction: the type source opens its own transaction, so
// no type read may happen while the list transaction is open, found or not.
func TestTypeIsReadOutsideTheTransaction(t *testing.T) {
	db := newFakeDB()
	l := db.seed(List{Name: "A", Type: ptr("wishlist")})
	svc := NewService(db, db, fixedClock{})
	if _, err := svc.Create(t.Context(), CreateInput{Name: "L", Type: ptr("movie")}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(t.Context(), CreateInput{Name: "L", Type: ptr("nope")}); err == nil {
		t.Fatal("Create with an unknown type: want an error")
	}
	typed, err := svc.Update(t.Context(), l.ID, 1, Patch{Type: Optional[string]{Set: true, Value: ptr("movie")}})
	if err != nil {
		t.Fatalf("Update with a type: %v", err)
	}
	if _, err := svc.Update(t.Context(), l.ID, typed.Version, Patch{Name: ptr("B")}); err != nil {
		t.Fatalf("Update without a type: %v", err)
	}
	if db.typeReadsInTx != 0 {
		t.Fatalf("%d type reads inside the list transaction, want 0", db.typeReadsInTx)
	}
}

// TestCreate_UnknownTypeBeforeDestination: the unknown type is reported before a
// missing destination, as it was when the type was read inside the transaction.
func TestCreate_UnknownTypeBeforeDestination(t *testing.T) {
	db := newFakeDB()
	trashed := uuid.New() // not an active folder
	_, err := NewService(db, db, fixedClock{}).Create(t.Context(), CreateInput{Name: "L", Type: ptr("nope"), FolderID: &trashed})
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "type" {
		t.Fatalf("err = %v, want a validation error on type", err)
	}
	if db.writes() {
		t.Fatalf("calls = %v, want no write", db.calls)
	}
}

// TestUpdate_UnknownTypeOrder: a stale version is reported before the unknown type,
// a current version reports the unknown type. Neither writes anything.
func TestUpdate_UnknownTypeOrder(t *testing.T) {
	tests := []struct {
		name    string
		version int
		wantVC  bool
	}{
		{"stale version is reported before the unknown type", 9, true},
		{"current version reports the unknown type", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			l := db.seed(List{Name: "A"})
			_, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, tt.version, Patch{Type: Optional[string]{Set: true, Value: ptr("nope")}})
			var vc *apperr.VersionConflictError
			var ve *apperr.ValidationError
			if tt.wantVC {
				if !errors.As(err, &vc) || vc.Current != 1 {
					t.Fatalf("err = %v, want version conflict at 1", err)
				}
			} else if !errors.As(err, &ve) || ve.Errors[0].Field != "type" {
				t.Fatalf("err = %v, want a validation error on type", err)
			}
			if db.writes() {
				t.Fatalf("calls = %v, want no write", db.calls)
			}
		})
	}
}

// TestUpdate_MissingListIsNotFoundFirst: a missing list is 404 before the type is
// looked at, even when the patch names an unknown type.
func TestUpdate_MissingListIsNotFoundFirst(t *testing.T) {
	db := newFakeDB()
	var nf *apperr.NotFoundError
	_, err := NewService(db, db, fixedClock{}).Update(t.Context(), uuid.New(), 1, Patch{Type: Optional[string]{Set: true, Value: ptr("nope")}})
	if !errors.As(err, &nf) || nf.Resource != "list" {
		t.Fatalf("err = %v, want not found (list)", err)
	}
}

// TestUpdate_TypeChangedBetweenReadAndLock: a type another write changed after the
// pre-transaction read is a version conflict, with or without a version bump.
func TestUpdate_TypeChangedBetweenReadAndLock(t *testing.T) {
	tests := []struct {
		name        string
		bumpVersion bool
		wantCurrent int
	}{
		{"the other write kept the version", false, 1},
		{"the other write bumped the version", true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB()
			l := db.seed(List{Name: "A", Type: ptr("wishlist")})
			db.beforeLockHook = func(f *fakeDB, id uuid.UUID) {
				cur := f.lists[id]
				cur.Type = ptr("movie")
				if tt.bumpVersion {
					cur.Version++
				}
				f.lists[id] = cur
			}
			_, err := NewService(db, db, fixedClock{}).Update(t.Context(), l.ID, 1, Patch{Name: ptr("B")})
			var vc *apperr.VersionConflictError
			if !errors.As(err, &vc) || vc.Current != tt.wantCurrent {
				t.Fatalf("err = %v, want version conflict at %d", err, tt.wantCurrent)
			}
			if db.writes() {
				t.Fatalf("calls = %v, want no write", db.calls)
			}
		})
	}
}
