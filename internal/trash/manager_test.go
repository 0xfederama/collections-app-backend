package trash_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
)

// The rules are tested against an in-memory fake of the Repo. It keeps the rows
// the SQL keeps (trash_batch_id, sort order, version), enforces the foreign keys
// the purge must respect (children before parents, no batch row still referenced)
// and logs every write, so the tests can assert the documented order and that
// files go after the commit.

type ffolder struct {
	seq, sort, version int
	name               string
	parent, batch      uuid.UUID // uuid.Nil parent = the workspace root, batch uuid.Nil = active
}

type flist struct {
	seq, sort, version int
	name               string
	folder, batch      uuid.UUID
}

type fitem struct {
	seq, sort, version int
	title              string
	list, batch        uuid.UUID
	file               *trash.AttachmentFile
}

type fbatch struct {
	seq            int
	rootType, name string
	rootID         uuid.UUID
	path           []trash.PathSegment
	at             time.Time
}

type fdata struct {
	seq       int
	folders   map[uuid.UUID]ffolder
	lists     map[uuid.UUID]flist
	items     map[uuid.UUID]fitem
	batches   map[uuid.UUID]fbatch
	retention *int
}

// fakeDB holds the rows, the write log (calls) and an optional injected failure
// for one write method (failOn), which rolls the transaction back.
type fakeDB struct {
	data   fdata
	calls  []string
	failOn string
}

func newFakeDB() *fakeDB {
	return &fakeDB{data: fdata{
		folders: map[uuid.UUID]ffolder{},
		lists:   map[uuid.UUID]flist{},
		items:   map[uuid.UUID]fitem{},
		batches: map[uuid.UUID]fbatch{},
	}}
}

func (d *fakeDB) nextSeq() int {
	d.data.seq++
	return d.data.seq
}

// Setup helpers. Ids are made by the test when a row must reference one before it exists.

func (d *fakeDB) addBatch(rootType string, rootID uuid.UUID, name string, at time.Time) uuid.UUID {
	id := uuid.New()
	d.data.batches[id] = fbatch{seq: d.nextSeq(), rootType: rootType, rootID: rootID, name: name, path: []trash.PathSegment{}, at: at}
	return id
}

func (d *fakeDB) putFolder(id uuid.UUID, name string, parent uuid.UUID, sort int, batch uuid.UUID) {
	d.data.folders[id] = ffolder{seq: d.nextSeq(), name: name, parent: parent, sort: sort, batch: batch}
}

func (d *fakeDB) putList(id uuid.UUID, name string, folder uuid.UUID, sort int, batch uuid.UUID) {
	d.data.lists[id] = flist{seq: d.nextSeq(), name: name, folder: folder, sort: sort, batch: batch}
}

func (d *fakeDB) putItem(id uuid.UUID, title string, list uuid.UUID, sort int, batch uuid.UUID) {
	d.data.items[id] = fitem{seq: d.nextSeq(), title: title, list: list, sort: sort, batch: batch}
}

func (d *fakeDB) folder(name string, parent uuid.UUID, sort int) uuid.UUID {
	id := uuid.New()
	d.putFolder(id, name, parent, sort, uuid.Nil)
	return id
}

func (d *fakeDB) list(name string, folder uuid.UUID, sort int) uuid.UUID {
	id := uuid.New()
	d.putList(id, name, folder, sort, uuid.Nil)
	return id
}

func (d *fakeDB) item(title string, list uuid.UUID, sort int) uuid.UUID {
	id := uuid.New()
	d.putItem(id, title, list, sort, uuid.Nil)
	return id
}

// batchByName maps every folder, list and item name (title) to its trash batch
// (uuid.Nil when active). The test names are unique across the three tables.
func (d *fakeDB) batchByName() map[string]uuid.UUID {
	out := map[string]uuid.UUID{}
	for _, f := range d.data.folders {
		out[f.name] = f.batch
	}
	for _, l := range d.data.lists {
		out[l.name] = l.batch
	}
	for _, i := range d.data.items {
		out[i.title] = i.batch
	}
	return out
}

// batchForRoot returns the id of the batch whose root is root.
func (d *fakeDB) batchForRoot(root uuid.UUID) (uuid.UUID, bool) {
	for id, b := range d.data.batches {
		if b.rootID == root {
			return id, true
		}
	}
	return uuid.Nil, false
}

func cloneData(in fdata) fdata {
	out := in
	out.folders = maps.Clone(in.folders)
	out.lists = maps.Clone(in.lists)
	out.items = maps.Clone(in.items)
	out.batches = maps.Clone(in.batches)
	return out
}

// fakeRepo is the Repo of one transaction (or of the pool, for the reads).
type fakeRepo struct{ db *fakeDB }

// write logs a write method and fails it when the test injected a failure for it.
func (r fakeRepo) write(op string) error {
	r.db.calls = append(r.db.calls, op)
	if r.db.failOn == op {
		return errors.New("injected failure: " + op)
	}
	return nil
}

func ptrOf(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func valueOf(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func (r fakeRepo) LockTree(ctx context.Context) error { return r.write("LockTree") }

func (r fakeRepo) InsertBatch(ctx context.Context, rootType string, rootID uuid.UUID, name string, path []trash.PathSegment, now time.Time) (uuid.UUID, error) {
	if err := r.write("InsertBatch"); err != nil {
		return uuid.Nil, err
	}
	if path == nil {
		path = []trash.PathSegment{}
	}
	id := uuid.New()
	r.db.data.batches[id] = fbatch{seq: r.db.nextSeq(), rootType: rootType, rootID: rootID, name: name, path: path, at: now}
	return id, nil
}

func (r fakeRepo) LockBatch(ctx context.Context, id uuid.UUID) (trash.Batch, error) {
	if err := r.write("LockBatch"); err != nil {
		return trash.Batch{}, err
	}
	b, ok := r.db.data.batches[id]
	if !ok {
		return trash.Batch{}, apperr.NotFound("trash entry")
	}
	return trash.Batch{ID: id, RootType: b.rootType, RootID: b.rootID, RootName: b.name, DeletedAt: b.at}, nil
}

func (r fakeRepo) DeleteBatch(ctx context.Context, id uuid.UUID) error {
	if err := r.write("DeleteBatch"); err != nil {
		return err
	}
	for _, f := range r.db.data.folders {
		if f.batch == id {
			return errors.New("batch still referenced by a folder")
		}
	}
	for _, l := range r.db.data.lists {
		if l.batch == id {
			return errors.New("batch still referenced by a list")
		}
	}
	for _, i := range r.db.data.items {
		if i.batch == id {
			return errors.New("batch still referenced by an item")
		}
	}
	delete(r.db.data.batches, id)
	return nil
}

func (r fakeRepo) DeleteOrphanBatches(ctx context.Context) error {
	if err := r.write("DeleteOrphanBatches"); err != nil {
		return err
	}
	for id, b := range r.db.data.batches {
		_, inF := r.db.data.folders[b.rootID]
		_, inL := r.db.data.lists[b.rootID]
		_, inI := r.db.data.items[b.rootID]
		if !inF && !inL && !inI {
			delete(r.db.data.batches, id)
		}
	}
	return nil
}

// batchesSorted returns the batch ids in creation order.
func (r fakeRepo) batchesSorted() []uuid.UUID {
	ids := slices.Collect(maps.Keys(r.db.data.batches))
	slices.SortFunc(ids, func(a, b uuid.UUID) int {
		return r.db.data.batches[a].seq - r.db.data.batches[b].seq
	})
	return ids
}

func (r fakeRepo) BatchIDs(ctx context.Context) ([]uuid.UUID, error) {
	return r.batchesSorted(), nil
}

func (r fakeRepo) ExpiredBatchIDs(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range r.batchesSorted() {
		if r.db.data.batches[id].at.Before(cutoff) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r fakeRepo) RetentionDays(ctx context.Context) (*int, error) {
	return r.db.data.retention, nil
}

func (r fakeRepo) ListBatches(ctx context.Context, limit, offset int, _ []uuid.UUID) ([]trash.Entry, error) {
	ids := r.batchesSorted()
	slices.Reverse(ids) // newest first
	slices.SortStableFunc(ids, func(a, b uuid.UUID) int {
		return r.db.data.batches[b].at.Compare(r.db.data.batches[a].at)
	})
	out := []trash.Entry{}
	for i := offset; i < len(ids) && len(out) < limit; i++ {
		b := r.db.data.batches[ids[i]]
		e := trash.Entry{ID: ids[i], RootType: b.rootType, RootID: b.rootID, RootName: b.name, FormerPath: b.path, DeletedAt: b.at}
		for _, f := range r.db.data.folders {
			if f.batch == ids[i] {
				e.Folders++
			}
		}
		for _, l := range r.db.data.lists {
			if l.batch == ids[i] {
				e.Lists++
			}
		}
		for _, it := range r.db.data.items {
			if it.batch == ids[i] {
				e.Items++
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func (r fakeRepo) CountBatches(ctx context.Context, _ []uuid.UUID) (int, error) {
	return len(r.db.data.batches), nil
}

// foldersSorted returns the folder ids in creation order.
func (r fakeRepo) foldersSorted() []uuid.UUID {
	ids := slices.Collect(maps.Keys(r.db.data.folders))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return r.db.data.folders[a].seq - r.db.data.folders[b].seq })
	return ids
}

func (r fakeRepo) listsSorted() []uuid.UUID {
	ids := slices.Collect(maps.Keys(r.db.data.lists))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return r.db.data.lists[a].seq - r.db.data.lists[b].seq })
	return ids
}

func (r fakeRepo) itemsSorted() []uuid.UUID {
	ids := slices.Collect(maps.Keys(r.db.data.items))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return r.db.data.items[a].seq - r.db.data.items[b].seq })
	return ids
}

func (r fakeRepo) FolderParents(ctx context.Context) ([]trash.FolderNode, error) {
	var out []trash.FolderNode
	for _, id := range r.foldersSorted() {
		f := r.db.data.folders[id]
		out = append(out, trash.FolderNode{ID: id, ParentID: ptrOf(f.parent), Name: f.name, Trashed: f.batch != uuid.Nil})
	}
	return out, nil
}

func (r fakeRepo) LockFolder(ctx context.Context, id uuid.UUID) (trash.FolderRow, error) {
	if err := r.write("LockFolder"); err != nil {
		return trash.FolderRow{}, err
	}
	f, ok := r.db.data.folders[id]
	if !ok || f.batch != uuid.Nil {
		return trash.FolderRow{}, apperr.NotFound("folder")
	}
	return trash.FolderRow{ID: id, Name: f.name, ParentID: ptrOf(f.parent), Version: f.version}, nil
}

func (r fakeRepo) LockList(ctx context.Context, id uuid.UUID) (trash.ListRow, error) {
	if err := r.write("LockList"); err != nil {
		return trash.ListRow{}, err
	}
	l, ok := r.db.data.lists[id]
	if !ok || l.batch != uuid.Nil {
		return trash.ListRow{}, apperr.NotFound("list")
	}
	return trash.ListRow{ID: id, Name: l.name, FolderID: ptrOf(l.folder), Version: l.version}, nil
}

func (r fakeRepo) LockItem(ctx context.Context, id uuid.UUID) (trash.ItemRow, error) {
	if err := r.write("LockItem"); err != nil {
		return trash.ItemRow{}, err
	}
	i, ok := r.db.data.items[id]
	if !ok || i.batch != uuid.Nil {
		return trash.ItemRow{}, apperr.NotFound("item")
	}
	return trash.ItemRow{ID: id, Title: i.title, ListID: i.list, Version: i.version}, nil
}

func (r fakeRepo) GetActiveFolder(ctx context.Context, id uuid.UUID) (trash.FolderRow, error) {
	f, ok := r.db.data.folders[id]
	if !ok || f.batch != uuid.Nil {
		return trash.FolderRow{}, apperr.NotFound("folder")
	}
	return trash.FolderRow{ID: id, Name: f.name, ParentID: ptrOf(f.parent), Version: f.version}, nil
}

func (r fakeRepo) GetActiveList(ctx context.Context, id uuid.UUID) (trash.ListRow, error) {
	l, ok := r.db.data.lists[id]
	if !ok || l.batch != uuid.Nil {
		return trash.ListRow{}, apperr.NotFound("list")
	}
	return trash.ListRow{ID: id, Name: l.name, FolderID: ptrOf(l.folder), Version: l.version}, nil
}

func (r fakeRepo) GetAnyFolderState(ctx context.Context, id uuid.UUID) (trash.ParentState, error) {
	f, ok := r.db.data.folders[id]
	if !ok {
		return trash.ParentState{}, apperr.NotFound("folder")
	}
	return trash.ParentState{Name: f.name, Trashed: f.batch != uuid.Nil}, nil
}

func (r fakeRepo) GetAnyListState(ctx context.Context, id uuid.UUID) (trash.ParentState, error) {
	l, ok := r.db.data.lists[id]
	if !ok {
		return trash.ParentState{}, apperr.NotFound("list")
	}
	return trash.ParentState{Name: l.name, Trashed: l.batch != uuid.Nil}, nil
}

// nextSort is the sort order after the highest of the given active rows, 0 when none.
func nextSort(sorts []int) int {
	if len(sorts) == 0 {
		return 0
	}
	return slices.Max(sorts) + 1
}

func (r fakeRepo) NextFolderSortOrder(ctx context.Context, parent *uuid.UUID) (int, error) {
	var sorts []int
	for _, f := range r.db.data.folders {
		if f.batch == uuid.Nil && f.parent == valueOf(parent) {
			sorts = append(sorts, f.sort)
		}
	}
	return nextSort(sorts), nil
}

func (r fakeRepo) NextListSortOrder(ctx context.Context, folder *uuid.UUID) (int, error) {
	var sorts []int
	for _, l := range r.db.data.lists {
		if l.batch == uuid.Nil && l.folder == valueOf(folder) {
			sorts = append(sorts, l.sort)
		}
	}
	return nextSort(sorts), nil
}

func (r fakeRepo) NextItemSortOrder(ctx context.Context, listID uuid.UUID) (int, error) {
	var sorts []int
	for _, i := range r.db.data.items {
		if i.batch == uuid.Nil && i.list == listID {
			sorts = append(sorts, i.sort)
		}
	}
	return nextSort(sorts), nil
}

func (r fakeRepo) MarkFolders(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.write("MarkFolders"); err != nil {
		return err
	}
	for _, id := range ids {
		if f := r.db.data.folders[id]; f.batch == uuid.Nil {
			f.batch = batch
			r.db.data.folders[id] = f
		}
	}
	return nil
}

func (r fakeRepo) MarkLists(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.write("MarkLists"); err != nil {
		return err
	}
	for _, id := range ids {
		if l := r.db.data.lists[id]; l.batch == uuid.Nil {
			l.batch = batch
			r.db.data.lists[id] = l
		}
	}
	return nil
}

func (r fakeRepo) MarkItems(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.write("MarkItems"); err != nil {
		return err
	}
	for _, id := range ids {
		if i := r.db.data.items[id]; i.batch == uuid.Nil {
			i.batch = batch
			r.db.data.items[id] = i
		}
	}
	return nil
}

func (r fakeRepo) ActiveListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range r.listsSorted() {
		l := r.db.data.lists[id]
		if l.batch == uuid.Nil && slices.Contains(folderIDs, l.folder) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r fakeRepo) ActiveItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range r.itemsSorted() {
		i := r.db.data.items[id]
		if i.batch == uuid.Nil && slices.Contains(listIDs, i.list) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r fakeRepo) GetTrashedFolder(ctx context.Context, id, batch uuid.UUID) (trash.FolderRow, error) {
	f, ok := r.db.data.folders[id]
	if !ok || f.batch != batch {
		return trash.FolderRow{}, apperr.NotFound("trash entry")
	}
	return trash.FolderRow{ID: id, Name: f.name, ParentID: ptrOf(f.parent), Version: f.version}, nil
}

func (r fakeRepo) GetTrashedList(ctx context.Context, id, batch uuid.UUID) (trash.ListRow, error) {
	l, ok := r.db.data.lists[id]
	if !ok || l.batch != batch {
		return trash.ListRow{}, apperr.NotFound("trash entry")
	}
	return trash.ListRow{ID: id, Name: l.name, FolderID: ptrOf(l.folder), Version: l.version}, nil
}

func (r fakeRepo) GetTrashedItem(ctx context.Context, id, batch uuid.UUID) (trash.ItemRow, error) {
	i, ok := r.db.data.items[id]
	if !ok || i.batch != batch {
		return trash.ItemRow{}, apperr.NotFound("trash entry")
	}
	return trash.ItemRow{ID: id, Title: i.title, ListID: i.list, Version: i.version}, nil
}

func (r fakeRepo) RestoreFolderRoot(ctx context.Context, id, batch uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.write("RestoreFolderRoot"); err != nil {
		return err
	}
	if f := r.db.data.folders[id]; f.batch == batch {
		f.parent, f.sort, f.batch = valueOf(parent), sortOrder, uuid.Nil
		f.version++
		r.db.data.folders[id] = f
	}
	return nil
}

func (r fakeRepo) RestoreListRoot(ctx context.Context, id, batch uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.write("RestoreListRoot"); err != nil {
		return err
	}
	if l := r.db.data.lists[id]; l.batch == batch {
		l.folder, l.sort, l.batch = valueOf(folder), sortOrder, uuid.Nil
		l.version++
		r.db.data.lists[id] = l
	}
	return nil
}

func (r fakeRepo) RestoreItemRoot(ctx context.Context, id, batch, listID uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.write("RestoreItemRoot"); err != nil {
		return err
	}
	if i := r.db.data.items[id]; i.batch == batch {
		i.list, i.sort, i.batch = listID, sortOrder, uuid.Nil
		i.version++
		r.db.data.items[id] = i
	}
	return nil
}

func (r fakeRepo) RestoreBatchFolders(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.write("RestoreBatchFolders"); err != nil {
		return err
	}
	for id, f := range r.db.data.folders {
		if f.batch == batch {
			f.batch = uuid.Nil
			r.db.data.folders[id] = f
		}
	}
	return nil
}

func (r fakeRepo) RestoreBatchLists(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.write("RestoreBatchLists"); err != nil {
		return err
	}
	for id, l := range r.db.data.lists {
		if l.batch == batch {
			l.batch = uuid.Nil
			r.db.data.lists[id] = l
		}
	}
	return nil
}

func (r fakeRepo) RestoreBatchItems(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.write("RestoreBatchItems"); err != nil {
		return err
	}
	for id, i := range r.db.data.items {
		if i.batch == batch {
			i.batch = uuid.Nil
			r.db.data.items[id] = i
		}
	}
	return nil
}

func (r fakeRepo) ListListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range r.listsSorted() {
		if slices.Contains(folderIDs, r.db.data.lists[id].folder) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r fakeRepo) ListItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range r.itemsSorted() {
		if slices.Contains(listIDs, r.db.data.items[id].list) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r fakeRepo) ListAttachmentFiles(ctx context.Context, itemIDs []uuid.UUID) ([]trash.AttachmentFile, error) {
	var out []trash.AttachmentFile
	for _, id := range itemIDs {
		if f := r.db.data.items[id].file; f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

func (r fakeRepo) DeleteContentVersions(ctx context.Context, itemIDs []uuid.UUID) error {
	return r.write("DeleteContentVersions")
}

func (r fakeRepo) DeleteLookupImports(ctx context.Context, itemIDs []uuid.UUID) error {
	return r.write("DeleteLookupImports")
}

func (r fakeRepo) DeleteItems(ctx context.Context, itemIDs []uuid.UUID) error {
	if err := r.write("DeleteItems"); err != nil {
		return err
	}
	for _, id := range itemIDs {
		delete(r.db.data.items, id)
	}
	return nil
}

func (r fakeRepo) DeleteLists(ctx context.Context, listIDs []uuid.UUID) error {
	if err := r.write("DeleteLists"); err != nil {
		return err
	}
	for _, i := range r.db.data.items {
		if slices.Contains(listIDs, i.list) {
			return errors.New("foreign key: an item still references the list")
		}
	}
	for _, id := range listIDs {
		delete(r.db.data.lists, id)
	}
	return nil
}

func (r fakeRepo) DeleteFolders(ctx context.Context, folderIDs []uuid.UUID) error {
	if err := r.write("DeleteFolders"); err != nil {
		return err
	}
	for _, l := range r.db.data.lists {
		if slices.Contains(folderIDs, l.folder) {
			return errors.New("foreign key: a list still references the folder")
		}
	}
	for id, f := range r.db.data.folders {
		if !slices.Contains(folderIDs, id) && slices.Contains(folderIDs, f.parent) {
			return errors.New("foreign key: a folder still references the folder")
		}
	}
	for _, id := range folderIDs {
		delete(r.db.data.folders, id)
	}
	return nil
}

func (r fakeRepo) DeleteFolderMembers(ctx context.Context, ids []uuid.UUID) error {
	return r.write("DeleteFolderMembers")
}

// The unit tests run without a user, so the in-transaction visibility checks never run.
func (r fakeRepo) FolderVisibleToCaller(context.Context, uuid.UUID) (bool, error) { return true, nil }
func (r fakeRepo) ListVisibleToCaller(context.Context, uuid.UUID) (bool, error)   { return true, nil }
func (r fakeRepo) FolderMemberIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (r fakeRepo) DeleteTopOrder(ctx context.Context, ids []uuid.UUID) error {
	return r.write("DeleteTopOrder")
}

func (r fakeRepo) DeleteAttachmentRows(ctx context.Context, ids []uuid.UUID) error {
	return r.write("DeleteAttachmentRows")
}

// fakeStore is the Store: a transaction commits its writes and rolls back on error.
type fakeStore struct{ db *fakeDB }

func (s fakeStore) InTx(ctx context.Context, fn func(trash.Repo) error) error {
	snap := cloneData(s.db.data)
	if err := fn(fakeRepo(s)); err != nil {
		s.db.data = snap
		s.db.calls = append(s.db.calls, "rollback")
		return err
	}
	s.db.calls = append(s.db.calls, "commit")
	return nil
}

func (s fakeStore) ListBatches(ctx context.Context, limit, offset int, only []uuid.UUID) ([]trash.Entry, error) {
	return fakeRepo(s).ListBatches(ctx, limit, offset, only)
}

func (s fakeStore) CountBatches(ctx context.Context, only []uuid.UUID) (int, error) {
	return fakeRepo(s).CountBatches(ctx, only)
}

func (s fakeStore) RetentionDays(ctx context.Context) (*int, error) {
	return fakeRepo(s).RetentionDays(ctx)
}

// recordingFiles logs every file delete into the same call log as the writes, so
// a test can check that files are deleted after the commit. With fail set, every
// delete errors.
type recordingFiles struct {
	storage.FileStore
	db   *fakeDB
	fail bool
}

func (f recordingFiles) Delete(key string) error {
	f.db.calls = append(f.db.calls, "delete:"+key)
	if f.fail {
		return errors.New("injected file failure")
	}
	return f.FileStore.Delete(key)
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// rig is a Service over the fake store, a temp-dir file store and a fixed clock.
type rig struct {
	ctx   context.Context
	db    *fakeDB
	local *storage.Local
	svc   *trash.Service
	logs  *bytes.Buffer
	now   time.Time
}

func newRig(t *testing.T, failFiles bool) rig {
	t.Helper()
	db := newFakeDB()
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	svc := trash.NewService(
		fakeStore{db: db},
		recordingFiles{FileStore: local, db: db, fail: failFiles},
		fixedClock{t: now},
		slog.New(slog.NewTextHandler(logs, nil)),
	)
	return rig{ctx: scope.With(context.Background(), uuid.New()), db: db, local: local, svc: svc, logs: logs, now: now}
}

// errKind names the error for the tables: "" for nil, else its kind and, for a
// validation, the first field; for a conflict, its code.
func errKind(err error) string {
	var (
		nf *apperr.NotFoundError
		vc *apperr.VersionConflictError
		ve *apperr.ValidationError
		ce *apperr.ConflictError
	)
	switch {
	case err == nil:
		return ""
	case errors.As(err, &nf):
		return "not_found"
	case errors.As(err, &vc):
		return "version_conflict"
	case errors.As(err, &ve):
		return "validation:" + ve.Errors[0].Field
	case errors.As(err, &ce):
		return "conflict:" + string(ce.Code)
	}
	return "other:" + err.Error()
}

// ---- delete ----

func TestDeleteFolderMarksSubtreeInOrder(t *testing.T) {
	r := newRig(t, false)
	db := r.db
	root := db.folder("Root", uuid.Nil, 0)
	a := db.folder("A", root, 0)
	b := db.folder("B", a, 0)
	l1 := db.list("L1", a, 0)
	l2 := db.list("L2", b, 0)
	db.item("I1", l1, 0)
	db.item("I2", l1, 1)
	db.item("I3", l2, 0)
	// An older batch nested below A: a trashed folder N with a list and an item.
	n := uuid.New()
	oldFolder := db.addBatch(trash.TypeFolder, n, "N", r.now.Add(-time.Hour))
	db.putFolder(n, "N", a, 1, oldFolder)
	nl := uuid.New()
	db.putList(nl, "NL", n, 0, oldFolder)
	db.putItem(uuid.New(), "NI", nl, 0, oldFolder)
	// A trashed list directly in A, from an older batch of its own.
	lt := uuid.New()
	oldList := db.addBatch(trash.TypeList, lt, "LT", r.now.Add(-time.Hour))
	db.putList(lt, "LT", a, 2, oldList)
	db.putItem(uuid.New(), "IT", lt, 0, oldList)

	if err := r.svc.DeleteFolder(r.ctx, a, 0); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	wantCalls := []string{"LockTree", "LockFolder", "InsertBatch", "MarkFolders", "MarkLists", "MarkItems", "commit"}
	if !slices.Equal(db.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", db.calls, wantCalls)
	}
	newBatch, ok := db.batchForRoot(a)
	if !ok {
		t.Fatal("no batch for A")
	}
	wantPath := []trash.PathSegment{{Type: trash.TypeFolder, ID: root, Name: "Root"}}
	if got := db.data.batches[newBatch].path; !slices.Equal(got, wantPath) {
		t.Errorf("path = %v, want %v", got, wantPath)
	}
	want := map[string]uuid.UUID{
		"Root": uuid.Nil,
		"A":    newBatch, "B": newBatch, "L1": newBatch, "L2": newBatch,
		"I1": newBatch, "I2": newBatch, "I3": newBatch,
		"N": oldFolder, "NL": oldFolder, "NI": oldFolder,
		"LT": oldList, "IT": oldList,
	}
	if got := db.batchByName(); !maps.Equal(got, want) {
		t.Errorf("batches by row:\n got %v\nwant %v", got, want)
	}
}

func TestDeleteListMarksItemsOnly(t *testing.T) {
	r := newRig(t, false)
	db := r.db
	f := db.folder("F", uuid.Nil, 0)
	l := db.list("L", f, 0)
	l2 := db.list("L2", f, 1)
	db.item("I1", l, 0)
	db.item("I2", l, 1)
	db.item("I3", l2, 0)
	older := uuid.New()
	oldBatch := db.addBatch(trash.TypeItem, older, "OLD", r.now.Add(-time.Hour))
	db.putItem(older, "OLD", l, 2, oldBatch)

	if err := r.svc.DeleteList(r.ctx, l, 0); err != nil {
		t.Fatalf("DeleteList: %v", err)
	}
	wantCalls := []string{"LockTree", "LockList", "InsertBatch", "MarkLists", "MarkItems", "commit"}
	if !slices.Equal(db.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", db.calls, wantCalls)
	}
	nb, _ := db.batchForRoot(l)
	want := map[string]uuid.UUID{"F": uuid.Nil, "L": nb, "L2": uuid.Nil, "I1": nb, "I2": nb, "I3": uuid.Nil, "OLD": oldBatch}
	if got := db.batchByName(); !maps.Equal(got, want) {
		t.Errorf("batches by row:\n got %v\nwant %v", got, want)
	}
}

func TestDeleteItemSnapshotsPath(t *testing.T) {
	cases := []struct {
		name string
		// build returns the item to delete and the former path it must snapshot.
		build func(db *fakeDB) (uuid.UUID, []trash.PathSegment)
	}{
		{"list at the root", func(db *fakeDB) (uuid.UUID, []trash.PathSegment) {
			l := db.list("L", uuid.Nil, 0)
			i := db.item("I", l, 0)
			return i, []trash.PathSegment{{Type: trash.TypeList, ID: l, Name: "L"}}
		}},
		{"list in nested folders", func(db *fakeDB) (uuid.UUID, []trash.PathSegment) {
			g := db.folder("G", uuid.Nil, 0)
			f := db.folder("F", g, 0)
			l := db.list("L", f, 0)
			i := db.item("I", l, 0)
			return i, []trash.PathSegment{
				{Type: trash.TypeFolder, ID: g, Name: "G"},
				{Type: trash.TypeFolder, ID: f, Name: "F"},
				{Type: trash.TypeList, ID: l, Name: "L"},
			}
		}},
		{"trashed ancestor ends the path", func(db *fakeDB) (uuid.UUID, []trash.PathSegment) {
			g := uuid.New()
			gb := db.addBatch(trash.TypeFolder, g, "G", time.Time{})
			db.putFolder(g, "G", uuid.Nil, 0, gb)
			f := db.folder("F", g, 0)
			l := db.list("L", f, 0)
			i := db.item("I", l, 0)
			return i, []trash.PathSegment{
				{Type: trash.TypeFolder, ID: f, Name: "F"},
				{Type: trash.TypeList, ID: l, Name: "L"},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			item, want := tc.build(r.db)
			if err := r.svc.DeleteItem(r.ctx, item, 0); err != nil {
				t.Fatalf("DeleteItem: %v", err)
			}
			b, ok := r.db.batchForRoot(item)
			if !ok {
				t.Fatal("no batch for the item")
			}
			if got := r.db.data.batches[b].path; !slices.Equal(got, want) {
				t.Errorf("path = %v, want %v", got, want)
			}
		})
	}
}

func TestDeleteRulesErrorOrder(t *testing.T) {
	cases := []struct {
		name    string
		kind    string // folder, list or item
		setup   func(db *fakeDB) uuid.UUID
		version int
		want    string
	}{
		{"folder missing", "folder", func(db *fakeDB) uuid.UUID { return uuid.New() }, 0, "not_found"},
		{"folder version stale", "folder", func(db *fakeDB) uuid.UUID { return db.folder("F", uuid.Nil, 0) }, 3, "version_conflict"},
		{"folder already trashed", "folder", func(db *fakeDB) uuid.UUID {
			id := uuid.New()
			b := db.addBatch(trash.TypeFolder, id, "F", time.Time{})
			db.putFolder(id, "F", uuid.Nil, 0, b)
			return id
		}, 0, "not_found"},
		{"list missing", "list", func(db *fakeDB) uuid.UUID { return uuid.New() }, 0, "not_found"},
		{"list version stale", "list", func(db *fakeDB) uuid.UUID { return db.list("L", uuid.Nil, 0) }, 1, "version_conflict"},
		{"item missing", "item", func(db *fakeDB) uuid.UUID { return uuid.New() }, 0, "not_found"},
		{"item version stale", "item", func(db *fakeDB) uuid.UUID {
			return db.item("I", db.list("L", uuid.Nil, 0), 0)
		}, 2, "version_conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			id := tc.setup(r.db)
			before := r.db.batchByName()
			var err error
			switch tc.kind {
			case "folder":
				err = r.svc.DeleteFolder(r.ctx, id, tc.version)
			case "list":
				err = r.svc.DeleteList(r.ctx, id, tc.version)
			case "item":
				err = r.svc.DeleteItem(r.ctx, id, tc.version)
			}
			if got := errKind(err); got != tc.want {
				t.Fatalf("error = %q, want %q (%v)", got, tc.want, err)
			}
			if slices.Contains(r.db.calls, "InsertBatch") {
				t.Error("a batch was inserted before the error")
			}
			if got := r.db.batchByName(); !maps.Equal(got, before) {
				t.Errorf("rows changed on error: %v", got)
			}
		})
	}
}

// ---- list ----

func TestListPagesNewestFirst(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
		wantNames             []string
	}{
		{"zero limit is the default", 0, 0, trash.DefaultLimit, 0, []string{"C", "B", "A"}},
		{"limit is capped", 100000, 0, trash.MaxLimit, 0, []string{"C", "B", "A"}},
		{"negative offset is zero", 2, -3, 2, 0, []string{"C", "B"}},
		{"second page", 2, 2, 2, 2, []string{"A"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			for i, name := range []string{"A", "B", "C"} {
				id := uuid.New()
				r.db.addBatch(trash.TypeFolder, id, name, r.now.Add(time.Duration(i)*time.Minute))
			}
			page, err := r.svc.List(r.ctx, tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if page.Limit != tc.wantLimit || page.Offset != tc.wantOffset || page.Total != 3 {
				t.Errorf("page = limit %d offset %d total %d, want limit %d offset %d total 3", page.Limit, page.Offset, page.Total, tc.wantLimit, tc.wantOffset)
			}
			var names []string
			for _, e := range page.Entries {
				names = append(names, e.RootName)
			}
			if !slices.Equal(names, tc.wantNames) {
				t.Errorf("entries = %v, want %v", names, tc.wantNames)
			}
		})
	}
}

// ---- restore ----

// trashedFolder trashes a folder under parent in its own batch and returns both ids.
func trashedFolder(db *fakeDB, name string, parent uuid.UUID, at time.Time) (uuid.UUID, uuid.UUID) {
	id := uuid.New()
	b := db.addBatch(trash.TypeFolder, id, name, at)
	db.putFolder(id, name, parent, 0, b)
	return id, b
}

// trashedList trashes a list in folder in its own batch and returns both ids.
func trashedList(db *fakeDB, name string, folder uuid.UUID, at time.Time) (uuid.UUID, uuid.UUID) {
	id := uuid.New()
	b := db.addBatch(trash.TypeList, id, name, at)
	db.putList(id, name, folder, 0, b)
	return id, b
}

// trashedItem trashes an item of list in its own batch and returns both ids.
func trashedItem(db *fakeDB, title string, list uuid.UUID, at time.Time) (uuid.UUID, uuid.UUID) {
	id := uuid.New()
	b := db.addBatch(trash.TypeItem, id, title, at)
	db.putItem(id, title, list, 0, b)
	return id, b
}

func TestRestoreFolderToOriginalParent(t *testing.T) {
	r := newRig(t, false)
	db := r.db
	root := db.folder("R", uuid.Nil, 0)
	db.folder("S1", root, 0)
	db.folder("S2", root, 4) // the next sort order after the active siblings is 5
	a, ba := trashedFolder(db, "A", root, r.now.Add(-time.Hour))
	bID := uuid.New()
	db.putFolder(bID, "B", a, 0, ba)
	la := uuid.New()
	db.putList(la, "LA", a, 0, ba)
	db.putItem(uuid.New(), "IA", la, 0, ba)

	got, err := r.svc.Restore(r.ctx, ba, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if want := (trash.Restored{RootType: trash.TypeFolder, RootID: a}); got != want {
		t.Errorf("restored = %+v, want %+v", got, want)
	}
	wantCalls := []string{"LockTree", "LockBatch", "RestoreFolderRoot", "RestoreBatchFolders", "RestoreBatchLists", "RestoreBatchItems", "DeleteBatch", "commit"}
	if !slices.Equal(db.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", db.calls, wantCalls)
	}
	f := db.data.folders[a]
	if f.parent != root || f.sort != 5 || f.version != 1 || f.batch != uuid.Nil {
		t.Errorf("A = parent %v sort %d version %d batch %v, want root, sort 5, version 1, active", f.parent == root, f.sort, f.version, f.batch)
	}
	if _, ok := db.data.batches[ba]; ok {
		t.Error("the batch row is still there")
	}
	for name, b := range db.batchByName() {
		if b != uuid.Nil {
			t.Errorf("%s is still in the trash", name)
		}
	}
	if v := db.data.folders[bID].version; v != 0 {
		t.Errorf("B version = %d, want unchanged 0", v)
	}
}

func TestRestoreIntoDestination(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r rig) (batch uuid.UUID, dest *trash.Destination, check func(db *fakeDB) bool)
	}{
		{"item into another list is appended last", func(r rig) (uuid.UUID, *trash.Destination, func(*fakeDB) bool) {
			src := r.db.folder("F", uuid.Nil, 0)
			srcList := r.db.list("L1", src, 0)
			dstList := r.db.list("L2", src, 1)
			r.db.item("X", dstList, 0)
			r.db.item("Y", dstList, 4)
			i, b := trashedItem(r.db, "I", srcList, r.now.Add(-time.Hour))
			return b, &trash.Destination{ListID: &dstList}, func(db *fakeDB) bool {
				it := db.data.items[i]
				return it.list == dstList && it.sort == 5 && it.version == 1 && it.batch == uuid.Nil
			}
		}},
		{"list into a folder is appended last", func(r rig) (uuid.UUID, *trash.Destination, func(*fakeDB) bool) {
			from := r.db.folder("From", uuid.Nil, 0)
			to := r.db.folder("To", uuid.Nil, 1)
			r.db.list("Existing", to, 2)
			l, b := trashedList(r.db, "L", from, r.now.Add(-time.Hour))
			return b, &trash.Destination{FolderSet: true, FolderID: &to}, func(db *fakeDB) bool {
				ll := db.data.lists[l]
				return ll.folder == to && ll.sort == 3 && ll.version == 1 && ll.batch == uuid.Nil
			}
		}},
		{"folder to the workspace root", func(r rig) (uuid.UUID, *trash.Destination, func(*fakeDB) bool) {
			parent := r.db.folder("P", uuid.Nil, 0)
			r.db.folder("Root1", uuid.Nil, 0)
			r.db.folder("Root2", uuid.Nil, 2)
			f, b := trashedFolder(r.db, "F", parent, r.now.Add(-time.Hour))
			return b, &trash.Destination{FolderSet: true}, func(db *fakeDB) bool {
				ff := db.data.folders[f]
				return ff.parent == uuid.Nil && ff.sort == 3 && ff.version == 1 && ff.batch == uuid.Nil
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			batch, dest, check := tc.setup(r)
			if _, err := r.svc.Restore(r.ctx, batch, dest); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if !check(r.db) {
				t.Errorf("restored row is not where it should be")
			}
		})
	}
}

func TestRestoreRules(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the batch to restore and the destination of the call.
		setup func(r rig) (batch uuid.UUID, dest *trash.Destination)
		want  string
	}{
		{"folder into itself is a cycle", func(r rig) (uuid.UUID, *trash.Destination) {
			a, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			return ba, &trash.Destination{FolderSet: true, FolderID: &a}
		}, "conflict:cycle_detected"},
		{"folder into a trashed descendant is a cycle", func(r rig) (uuid.UUID, *trash.Destination) {
			a, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			b := uuid.New()
			r.db.putFolder(b, "B", a, 0, ba)
			return ba, &trash.Destination{FolderSet: true, FolderID: &b}
		}, "conflict:cycle_detected"},
		{"folder into a missing folder is unavailable", func(r rig) (uuid.UUID, *trash.Destination) {
			_, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			missing := uuid.New()
			return ba, &trash.Destination{FolderSet: true, FolderID: &missing}
		}, "conflict:destination_unavailable"},
		{"folder into a trashed folder is unavailable", func(r rig) (uuid.UUID, *trash.Destination) {
			_, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			x, _ := trashedFolder(r.db, "X", uuid.Nil, r.now)
			return ba, &trash.Destination{FolderSet: true, FolderID: &x}
		}, "conflict:destination_unavailable"},
		{"folder without destination and a trashed parent is blocked", func(r rig) (uuid.UUID, *trash.Destination) {
			p, _ := trashedFolder(r.db, "P", uuid.Nil, r.now)
			_, ba := trashedFolder(r.db, "A", p, r.now)
			return ba, nil
		}, "conflict:parent_in_trash"},
		{"list without destination and a trashed folder is blocked", func(r rig) (uuid.UUID, *trash.Destination) {
			f, _ := trashedFolder(r.db, "F", uuid.Nil, r.now)
			_, bl := trashedList(r.db, "L", f, r.now)
			return bl, nil
		}, "conflict:parent_in_trash"},
		{"item without destination and a trashed list is blocked", func(r rig) (uuid.UUID, *trash.Destination) {
			l, _ := trashedList(r.db, "L", uuid.Nil, r.now)
			_, bi := trashedItem(r.db, "I", l, r.now)
			return bi, nil
		}, "conflict:parent_in_trash"},
		{"item into a folder is a validation error", func(r rig) (uuid.UUID, *trash.Destination) {
			l, _ := trashedList(r.db, "L", uuid.Nil, r.now)
			_, bi := trashedItem(r.db, "I", l, r.now)
			return bi, &trash.Destination{FolderSet: true}
		}, "validation:destination.folder_id"},
		{"item without list_id is a validation error", func(r rig) (uuid.UUID, *trash.Destination) {
			l, _ := trashedList(r.db, "L", uuid.Nil, r.now)
			_, bi := trashedItem(r.db, "I", l, r.now)
			return bi, &trash.Destination{}
		}, "validation:destination.list_id"},
		{"item into a missing list is unavailable", func(r rig) (uuid.UUID, *trash.Destination) {
			l, _ := trashedList(r.db, "L", uuid.Nil, r.now)
			_, bi := trashedItem(r.db, "I", l, r.now)
			missing := uuid.New()
			return bi, &trash.Destination{ListID: &missing}
		}, "conflict:destination_unavailable"},
		{"folder with list_id is a validation error", func(r rig) (uuid.UUID, *trash.Destination) {
			_, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			l := uuid.New()
			return ba, &trash.Destination{FolderSet: true, ListID: &l}
		}, "validation:destination.list_id"},
		{"folder without folder_id is a validation error", func(r rig) (uuid.UUID, *trash.Destination) {
			_, ba := trashedFolder(r.db, "A", uuid.Nil, r.now)
			return ba, &trash.Destination{}
		}, "validation:destination.folder_id"},
		{"list with list_id is a validation error", func(r rig) (uuid.UUID, *trash.Destination) {
			f, _ := trashedFolder(r.db, "F", uuid.Nil, r.now)
			_, bl := trashedList(r.db, "L", f, r.now)
			l := uuid.New()
			return bl, &trash.Destination{ListID: &l}
		}, "validation:destination.list_id"},
		{"list into a missing folder is unavailable", func(r rig) (uuid.UUID, *trash.Destination) {
			f, _ := trashedFolder(r.db, "F", uuid.Nil, r.now)
			_, bl := trashedList(r.db, "L", f, r.now)
			missing := uuid.New()
			return bl, &trash.Destination{FolderSet: true, FolderID: &missing}
		}, "conflict:destination_unavailable"},
		{"unknown batch is not found", func(r rig) (uuid.UUID, *trash.Destination) {
			return uuid.New(), nil
		}, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			batch, dest := tc.setup(r)
			before := r.db.batchByName()
			beforeBatches := len(r.db.data.batches)
			_, err := r.svc.Restore(r.ctx, batch, dest)
			if got := errKind(err); got != tc.want {
				t.Fatalf("error = %q, want %q (%v)", got, tc.want, err)
			}
			if got := len(r.db.data.batches); got != beforeBatches {
				t.Errorf("batches = %d after the error, want %d", got, beforeBatches)
			}
			if got := r.db.batchByName(); !maps.Equal(got, before) {
				t.Errorf("rows changed on error: %v", got)
			}
		})
	}
}

func TestRestoreBlockedNamesTheBlockingParent(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r rig) (batch uuid.UUID, blocker uuid.UUID)
		typ   string
		label string
	}{
		{"folder blocked by its folder", func(r rig) (uuid.UUID, uuid.UUID) {
			p, _ := trashedFolder(r.db, "Parent", uuid.Nil, r.now)
			_, ba := trashedFolder(r.db, "A", p, r.now)
			return ba, p
		}, trash.TypeFolder, "Parent"},
		{"list blocked by its folder", func(r rig) (uuid.UUID, uuid.UUID) {
			f, _ := trashedFolder(r.db, "Folder", uuid.Nil, r.now)
			_, bl := trashedList(r.db, "L", f, r.now)
			return bl, f
		}, trash.TypeFolder, "Folder"},
		{"item blocked by its list", func(r rig) (uuid.UUID, uuid.UUID) {
			l, _ := trashedList(r.db, "Shelf", uuid.Nil, r.now)
			_, bi := trashedItem(r.db, "I", l, r.now)
			return bi, l
		}, trash.TypeList, "Shelf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, false)
			batch, blocker := tc.setup(r)
			_, err := r.svc.Restore(r.ctx, batch, nil)
			var ce *apperr.ConflictError
			if !errors.As(err, &ce) || ce.Blocking == nil {
				t.Fatalf("error = %v, want a parent_in_trash conflict with the blocking parent", err)
			}
			if ce.Blocking.Type != tc.typ || ce.Blocking.ID != blocker.String() || ce.Blocking.Name != tc.label {
				t.Errorf("blocking = %+v, want type %s id %s name %s", *ce.Blocking, tc.typ, blocker, tc.label)
			}
		})
	}
}
