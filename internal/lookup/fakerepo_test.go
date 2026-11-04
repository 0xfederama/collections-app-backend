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
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// fakeRepo is an in-memory Repo for the apply and maintenance rules. It keeps every
// write in order and the ones made outside a transaction, and it rolls a failed
// transaction back to the state before it (maps are copied per InTx), like
// Postgres does. The SQL filter of the weekly pass is mirrored in ScheduledItems.
type fakeRepo struct {
	inTx    bool
	commits int
	writes  []string // every write and lock, in order
	outside []string // the ones made outside a transaction (must stay empty)

	lists       map[uuid.UUID]*string
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

// ScheduledItems mirrors ListScheduledTMDBItems: movie and TV items with a numeric
// tmdb_id that are not trashed and not lookup_disabled, and whose provenance (if
// any) was fetched before fetchedBefore; never-fetched items sort first.
func (f *fakeRepo) ScheduledItems(_ context.Context, fetchedBefore time.Time, limit int32) ([]scheduledItem, error) {
	type candidate struct {
		item      scheduledItem
		fetchedAt *time.Time
	}
	var due []candidate
	for id, it := range f.items {
		if it.deleted || (it.typ == nil || (*it.typ != "movie" && *it.typ != "tv_show")) {
			continue
		}
		var metadata map[string]any
		if json.Unmarshal(it.metadata, &metadata) != nil {
			continue
		}
		if _, ok := metadata["tmdb_id"].(float64); !ok {
			continue
		}
		if disabled, _ := metadata["lookup_disabled"].(bool); disabled {
			continue
		}
		c := candidate{item: scheduledItem{ItemID: id, ItemVersion: it.version}}
		if row, ok := f.imports[id]; ok {
			if row.Provider != ProviderTMDB || row.FetchedAt.After(fetchedBefore) {
				continue
			}
			c.fetchedAt = &row.FetchedAt
		}
		due = append(due, c)
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := due[i].fetchedAt, due[j].fetchedAt
		switch {
		case a == nil || b == nil:
			if a == nil && b == nil {
				return due[i].item.ItemID.String() < due[j].item.ItemID.String()
			}
			return a == nil
		case !a.Equal(*b):
			return a.Before(*b)
		}
		return due[i].item.ItemID.String() < due[j].item.ItemID.String()
	})
	out := make([]scheduledItem, 0, len(due))
	for _, c := range due {
		out = append(out, c.item)
	}
	if len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
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
	list := uuid.New()
	repo.lists[list] = ptr("movie")
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
