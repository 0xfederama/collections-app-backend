package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// fakeRepo is an in-memory Repo for the apply and retention rules. It keeps every
// write in order and the ones made outside a transaction, and it rolls a failed
// transaction back to the state before it (maps are copied per InTx), like
// Postgres does. The SQL filters (provider, deadlines, workspace) are mirrored in
// ExpiredImportIDs and DueImports.
type fakeRepo struct {
	inTx    bool
	commits int
	writes  []string // every write and lock, in order
	outside []string // the ones made outside a transaction (must stay empty)

	lists       map[uuid.UUID]*string
	defs        map[string]typeDef
	fields      map[uuid.UUID][]types.Field
	items       map[uuid.UUID]fakeItem
	imports     map[uuid.UUID]importSave
	attachments map[uuid.UUID]images.Attachment
	nextOrder   int32

	// failInsertItem makes InsertItem fail (a rollback test).
	failInsertItem error
}

// fakeItem is one item row as the fake stores it.
type fakeItem struct {
	listID          uuid.UUID
	version         int
	title           string
	typ             *string
	metadata        []byte
	imageURL        *string
	imageAttachment uuid.NullUUID
	deleted         bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		lists:       map[uuid.UUID]*string{},
		defs:        map[string]typeDef{},
		fields:      map[uuid.UUID][]types.Field{},
		items:       map[uuid.UUID]fakeItem{},
		imports:     map[uuid.UUID]importSave{},
		attachments: map[uuid.UUID]images.Attachment{},
	}
}

func (f *fakeRepo) write(name string) {
	f.writes = append(f.writes, name)
	if !f.inTx {
		f.outside = append(f.outside, name)
	}
}

type fakeSnapshot struct {
	items       map[uuid.UUID]fakeItem
	imports     map[uuid.UUID]importSave
	attachments map[uuid.UUID]images.Attachment
}

func (f *fakeRepo) snapshot() fakeSnapshot {
	s := fakeSnapshot{
		items:       make(map[uuid.UUID]fakeItem, len(f.items)),
		imports:     make(map[uuid.UUID]importSave, len(f.imports)),
		attachments: make(map[uuid.UUID]images.Attachment, len(f.attachments)),
	}
	for k, v := range f.items {
		s.items[k] = v
	}
	for k, v := range f.imports {
		s.imports[k] = v
	}
	for k, v := range f.attachments {
		s.attachments[k] = v
	}
	return s
}

func (f *fakeRepo) restore(s fakeSnapshot) {
	f.items, f.imports, f.attachments = s.items, s.imports, s.attachments
}

func (f *fakeRepo) InTx(_ context.Context, fn func(Repo) error) error {
	before := f.snapshot()
	f.inTx = true
	err := fn(f)
	f.inTx = false
	if err != nil {
		f.restore(before)
		return err
	}
	f.commits++
	return nil
}

func (f *fakeRepo) ListType(_ context.Context, listID uuid.UUID) (*string, error) {
	typ, ok := f.lists[listID]
	if !ok {
		return nil, apperr.NotFound("list")
	}
	return typ, nil
}

func (f *fakeRepo) Import(_ context.Context, itemID uuid.UUID) (Record, bool, error) {
	row, ok := f.imports[itemID]
	if !ok {
		return Record{}, false, nil
	}
	fields := map[string]any{}
	if err := json.Unmarshal(row.ImportedFields, &fields); err != nil {
		return Record{}, false, err
	}
	return Record{
		ItemID: itemID, ItemVersion: f.items[itemID].version, Provider: row.Provider, SourceID: row.SourceID, Type: row.Type,
		ImportedFields: fields, ImportedTitle: row.ImportedTitle, OriginalTitle: row.OriginalTitle,
		AttachmentID: nullUUIDPtr(row.AttachmentID), PosterPath: valueOrEmpty(row.PosterPath),
		PosterFetchedAt: row.PosterFetchedAt, FetchedAt: row.FetchedAt,
	}, true, nil
}

func (f *fakeRepo) LockTree(context.Context) error {
	f.write("lock tree")
	return nil
}

func (f *fakeRepo) NextSortOrder(context.Context, uuid.UUID) (int32, error) {
	return f.nextOrder, nil
}

func (f *fakeRepo) LockItem(_ context.Context, itemID uuid.UUID) (lockedItem, error) {
	it, ok := f.items[itemID]
	if !ok || it.deleted {
		return lockedItem{}, apperr.NotFound("item")
	}
	f.write("lock item")
	return lockedItem{
		Version: it.version, Title: it.title, Type: it.typ, Metadata: it.metadata,
		ImageURL: it.imageURL, ImageAttachmentID: it.imageAttachment,
	}, nil
}

func (f *fakeRepo) InsertItem(_ context.Context, in newItem) (uuid.UUID, error) {
	f.write("insert item")
	if f.failInsertItem != nil {
		return uuid.Nil, f.failInsertItem
	}
	id := uuid.New()
	f.items[id] = fakeItem{
		listID: in.ListID, version: 1, title: in.Title, typ: f.lists[in.ListID], metadata: in.Metadata,
		imageAttachment: in.ImageAttachmentID,
	}
	return id, nil
}

func (f *fakeRepo) UpdateItem(_ context.Context, itemID uuid.UUID, u itemUpdate) error {
	f.write("update item")
	it := f.items[itemID]
	it.title, it.metadata, it.imageURL, it.imageAttachment = u.Title, u.Metadata, u.ImageURL, u.ImageAttachmentID
	it.version++
	f.items[itemID] = it
	return nil
}

func (f *fakeRepo) SaveImport(_ context.Context, in importSave) error {
	f.write("save import")
	f.imports[in.ItemID] = in
	return nil
}

func (f *fakeRepo) TypeByKey(_ context.Context, key string) (typeDef, bool, error) {
	def, ok := f.defs[key]
	return def, ok, nil
}

func (f *fakeRepo) TypeFields(_ context.Context, typeID uuid.UUID) ([]types.Field, error) {
	return f.fields[typeID], nil
}

func (f *fakeRepo) InsertAttachment(_ context.Context, a images.Attachment, _ time.Time) error {
	f.write("insert attachment")
	f.attachments[a.ID] = a
	return nil
}

func (f *fakeRepo) AttachmentExists(_ context.Context, id uuid.UUID) (bool, error) {
	_, ok := f.attachments[id]
	return ok, nil
}

func (f *fakeRepo) DeleteAttachment(_ context.Context, id uuid.UUID) (images.FileKeys, error) {
	f.write("delete attachment")
	a, ok := f.attachments[id]
	if !ok {
		return images.FileKeys{}, errors.New("attachment not found")
	}
	delete(f.attachments, id)
	return images.FileKeys{StorageKey: a.StorageKey, ThumbnailStorageKey: a.ThumbnailStorageKey}, nil
}

func (f *fakeRepo) ExpiredImportIDs(_ context.Context, now time.Time, limit int32) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for id, row := range f.imports {
		if row.Provider != ProviderTMDB {
			continue
		}
		if (row.ExpiresAt != nil && !row.ExpiresAt.After(now)) || (row.PosterExpiresAt != nil && !row.PosterExpiresAt.After(now)) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	if len(ids) > int(limit) {
		ids = ids[:limit]
	}
	return ids, nil
}

func (f *fakeRepo) DueImports(_ context.Context, now time.Time, limit int32) ([]dueImport, error) {
	var out []dueImport
	for id, row := range f.imports {
		if row.Provider != ProviderTMDB {
			continue
		}
		refreshDue := row.RefreshAfter != nil && !row.RefreshAfter.After(now)
		posterDue := row.PosterRefreshAfter != nil && !row.PosterRefreshAfter.After(now)
		if !refreshDue && !posterDue {
			continue
		}
		it := f.items[id]
		out = append(out, dueImport{
			ItemID: id, ItemVersion: it.version, Type: row.Type, SourceID: row.SourceID, Metadata: it.metadata,
			DeletedAt: deletedAt(it), CurrentType: it.typ,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID.String() < out[j].ItemID.String() })
	if len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}

func deletedAt(it fakeItem) *time.Time {
	if !it.deleted {
		return nil
	}
	t := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return &t
}

func (f *fakeRepo) LockImportedItem(_ context.Context, itemID uuid.UUID) (lockedItem, bool, error) {
	row, ok := f.imports[itemID]
	if !ok {
		return lockedItem{}, false, nil
	}
	f.write("lock imported item")
	it := f.items[itemID]
	return lockedItem{
		Version: it.version, Title: it.title, Type: it.typ, Metadata: it.metadata,
		ImageURL: it.imageURL, ImageAttachmentID: it.imageAttachment,
		ImportedFields: row.ImportedFields, ImportedTitle: row.ImportedTitle, OriginalTitle: row.OriginalTitle,
		ImportedAttachmentID: row.AttachmentID,
	}, true, nil
}

func (f *fakeRepo) ImportDeadlines(_ context.Context, itemID uuid.UUID) (importDeadlines, bool, error) {
	row, ok := f.imports[itemID]
	return importDeadlines{ExpiresAt: row.ExpiresAt, PosterExpiresAt: row.PosterExpiresAt}, ok, nil
}

func (f *fakeRepo) ClearPosterProvenance(_ context.Context, itemID uuid.UUID) error {
	f.write("clear poster provenance")
	row := f.imports[itemID]
	row.AttachmentID = uuid.NullUUID{}
	row.PosterPath, row.PosterFetchedAt, row.PosterRefreshAfter, row.PosterExpiresAt = nil, nil, nil, nil
	f.imports[itemID] = row
	return nil
}

// RetainPosterAfterMetadataExpiry follows the SQL: it applies only when the
// poster deadlines are all set.
func (f *fakeRepo) RetainPosterAfterMetadataExpiry(_ context.Context, itemID uuid.UUID) error {
	f.write("retain poster provenance")
	row := f.imports[itemID]
	if row.PosterFetchedAt == nil || row.PosterRefreshAfter == nil || row.PosterExpiresAt == nil {
		return nil
	}
	row.ImportedFields = []byte("{}")
	row.ImportedTitle = nil
	row.FetchedAt = *row.PosterFetchedAt
	row.RefreshAfter = row.PosterRefreshAfter
	row.ExpiresAt = row.PosterExpiresAt
	f.imports[itemID] = row
	return nil
}

func (f *fakeRepo) DeleteImport(_ context.Context, itemID uuid.UUID) error {
	f.write("delete import")
	delete(f.imports, itemID)
	return nil
}

// repoItems reads items from the fake after a commit (the itemReader of DBStore).
type repoItems struct{ repo *fakeRepo }

func (g repoItems) Get(_ context.Context, id uuid.UUID) (items.Item, error) {
	it, ok := g.repo.items[id]
	if !ok {
		return items.Item{}, apperr.NotFound("item")
	}
	return items.Item{ID: id, ListID: it.listID, Title: it.title, Version: it.version}, nil
}

// deleteLog records each file delete with the transaction state it happened in.
type deleteLog struct {
	storage.FileStore
	repo    *fakeRepo
	deletes []fileDelete
}

type fileDelete struct {
	key     string
	inTx    bool
	commits int
}

func (d *deleteLog) Delete(key string) error {
	d.deletes = append(d.deletes, fileDelete{key: key, inTx: d.repo.inTx, commits: d.repo.commits})
	return d.FileStore.Delete(key)
}

// applyEnv is a DBStore over the fake repository and a real local file store.
type applyEnv struct {
	t     *testing.T
	repo  *fakeRepo
	files *deleteLog
	clock *fakeClock
	store *DBStore
	list  uuid.UUID
}

func newApplyEnv(t *testing.T, now time.Time) *applyEnv {
	t.Helper()
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	repo := newFakeRepo()
	files := &deleteLog{FileStore: local, repo: repo}
	clk := &fakeClock{now: now}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	list, movie := uuid.New(), uuid.New()
	repo.lists[list] = ptr("movie")
	repo.defs["movie"] = typeDef{ID: movie}
	repo.fields[movie] = []types.Field{
		{Key: "release_year", Type: "number"},
		{Key: "director", Type: "text"},
		{Key: "runtime_minutes", Type: "number"},
		{Key: "tmdb_id", Type: "number", Special: true},
		{Key: "lookup_disabled", Type: "boolean", Special: true},
	}
	store := newStore(repo, files, clk, repoItems{repo}, logger, uuid.New())
	return &applyEnv{t: t, repo: repo, files: files, clock: clk, store: store, list: list}
}

// addItem puts an active item of the list into the fake, with the list's type.
func (e *applyEnv) addItem(title string, metadata map[string]any) uuid.UUID {
	e.t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		e.t.Fatal(err)
	}
	if metadata == nil {
		raw = []byte("{}")
	}
	id := uuid.New()
	e.repo.items[id] = fakeItem{listID: e.list, version: 1, title: title, typ: e.repo.lists[e.list], metadata: raw}
	return id
}

// edit changes one stored item in place (test setup only).
func (f *fakeRepo) edit(itemID uuid.UUID, fn func(*fakeItem)) {
	it := f.items[itemID]
	fn(&it)
	f.items[itemID] = it
}

// countFiles counts the files on disk under images/.
func countFiles(t *testing.T, e *applyEnv) int {
	t.Helper()
	n := 0
	if err := e.files.Walk("images", func(string, time.Time) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// writesEqual compares the recorded writes with want, in order.
func writesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
