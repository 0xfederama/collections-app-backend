package images_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// state is what the fake database holds: attachment rows with their creation
// time, and the image part of each active item.
type state struct {
	attachments map[uuid.UUID]images.Attachment
	created     map[uuid.UUID]time.Time
	items       map[uuid.UUID]images.ItemImageState
}

func (st state) clone() state {
	c := state{
		attachments: make(map[uuid.UUID]images.Attachment, len(st.attachments)),
		created:     make(map[uuid.UUID]time.Time, len(st.created)),
		items:       make(map[uuid.UUID]images.ItemImageState, len(st.items)),
	}
	for k, v := range st.attachments {
		c.attachments[k] = v
	}
	for k, v := range st.created {
		c.created[k] = v
	}
	for k, v := range st.items {
		c.items[k] = v
	}
	return c
}

// referenced reports whether an item points at the attachment.
func (st state) referenced(id uuid.UUID) bool {
	for _, it := range st.items {
		if it.AttachmentID != nil && *it.AttachmentID == id {
			return true
		}
	}
	return false
}

func (st *state) insert(a images.Attachment, now time.Time) {
	st.attachments[a.ID] = a
	st.created[a.ID] = now
}

func (st state) get(id uuid.UUID) (images.Attachment, error) {
	a, ok := st.attachments[id]
	if !ok {
		return images.Attachment{}, apperr.NotFound("attachment")
	}
	return a, nil
}

// del removes an unreferenced attachment (the SQL guard: NotFound when referenced or gone).
func (st *state) del(id uuid.UUID) (images.FileKeys, error) {
	a, ok := st.attachments[id]
	if !ok || st.referenced(id) {
		return images.FileKeys{}, apperr.NotFound("attachment")
	}
	delete(st.attachments, id)
	delete(st.created, id)
	return images.FileKeys{StorageKey: a.StorageKey, ThumbnailStorageKey: a.ThumbnailStorageKey}, nil
}

func (st state) unreferenced(cutoff time.Time) []uuid.UUID {
	var ids []uuid.UUID
	for id := range st.attachments {
		if !st.referenced(id) && st.created[id].Before(cutoff) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (st state) keys() []string {
	var ks []string
	for _, a := range st.attachments {
		ks = append(ks, a.StorageKey, a.ThumbnailStorageKey)
	}
	return ks
}

func (st state) lock(itemID uuid.UUID) (images.ItemImageState, error) {
	it, ok := st.items[itemID]
	if !ok {
		return images.ItemImageState{}, apperr.NotFound("item")
	}
	return it, nil
}

// fakeRepo runs the Repo methods against one transaction's copy of the state.
type fakeRepo struct{ st *state }

func (r fakeRepo) InsertAttachment(_ context.Context, a images.Attachment, now time.Time) error {
	r.st.insert(a, now)
	return nil
}

func (r fakeRepo) GetAttachment(_ context.Context, id uuid.UUID) (images.Attachment, error) {
	return r.st.get(id)
}

func (r fakeRepo) DeleteAttachment(_ context.Context, id uuid.UUID) (images.FileKeys, error) {
	return r.st.del(id)
}

func (r fakeRepo) ListAttachmentKeys(context.Context) ([]string, error) { return r.st.keys(), nil }

func (r fakeRepo) ListUnreferencedAttachments(_ context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	return r.st.unreferenced(cutoff), nil
}

func (r fakeRepo) LockItem(_ context.Context, itemID uuid.UUID) (images.ItemImageState, error) {
	return r.st.lock(itemID)
}

func (r fakeRepo) SetItemImageAttachment(_ context.Context, itemID, attachmentID uuid.UUID, _ time.Time) error {
	it := r.st.items[itemID]
	it.Version++
	it.URL = nil
	it.AttachmentID = &attachmentID
	r.st.items[itemID] = it
	return nil
}

func (r fakeRepo) SetItemImageURL(_ context.Context, itemID uuid.UUID, url string, _ time.Time) error {
	it := r.st.items[itemID]
	it.Version++
	it.AttachmentID = nil
	it.URL = &url
	r.st.items[itemID] = it
	return nil
}

func (r fakeRepo) ClearItemImage(_ context.Context, itemID uuid.UUID, _ time.Time) error {
	it := r.st.items[itemID]
	it.Version++
	it.AttachmentID = nil
	it.URL = nil
	r.st.items[itemID] = it
	return nil
}

// fakeStore keeps the committed state. A transaction works on a copy and is
// applied only when fn succeeds and commitErr is nil (a failed commit rolls back).
type fakeStore struct {
	cur       state
	commitErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{cur: state{
		attachments: map[uuid.UUID]images.Attachment{},
		created:     map[uuid.UUID]time.Time{},
		items:       map[uuid.UUID]images.ItemImageState{},
	}}
}

func (s *fakeStore) InTx(_ context.Context, fn func(images.Repo) error) error {
	tx := s.cur.clone()
	if err := fn(fakeRepo{st: &tx}); err != nil {
		return err
	}
	if s.commitErr != nil {
		return s.commitErr
	}
	s.cur = tx
	return nil
}

func (s *fakeStore) GetAttachment(_ context.Context, id uuid.UUID) (images.Attachment, error) {
	return s.cur.get(id)
}

func (s *fakeStore) DeleteAttachment(_ context.Context, id uuid.UUID) (images.FileKeys, error) {
	return s.cur.del(id)
}

func (s *fakeStore) ListAttachmentKeys(context.Context) ([]string, error) { return s.cur.keys(), nil }

func (s *fakeStore) ListUnreferencedAttachments(_ context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	return s.cur.unreferenced(cutoff), nil
}

// fakeItems answers the item read every image write returns.
type fakeItems struct{}

func (fakeItems) Get(context.Context, uuid.UUID) (items.Item, error) { return items.Item{}, nil }

// fakeClock is a settable clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// faultyFiles fails every Delete while failDelete is set, without deleting.
type faultyFiles struct {
	storage.FileStore
	failDelete bool
}

func (f *faultyFiles) Delete(key string) error {
	if f.failDelete {
		return errors.New("disk on fire")
	}
	return f.FileStore.Delete(key)
}

// harness wires Service over the fake store, a temp-dir FileStore and a fake clock.
type harness struct {
	t     *testing.T
	ctx   context.Context
	store *fakeStore
	files *faultyFiles
	clock *fakeClock
	svc   *images.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	h := &harness{
		t:     t,
		ctx:   scope.With(t.Context(), uuid.New()),
		store: newFakeStore(),
		files: &faultyFiles{FileStore: local},
		// The walk compares real file times, so the fake clock starts from the real one.
		clock: &fakeClock{now: time.Now().UTC()},
	}
	h.svc = images.NewService(h.store, h.files, fakeItems{}, h.clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h
}

// addItem adds an active item at version v.
func (h *harness) addItem(v int) uuid.UUID {
	id := uuid.New()
	h.store.cur.items[id] = images.ItemImageState{Version: v}
	return id
}

// putFile writes a file under key and fails the test on error.
func (h *harness) putFile(key string) {
	h.t.Helper()
	if _, err := h.files.Put(key, strings.NewReader("bytes of "+key)); err != nil {
		h.t.Fatal(err)
	}
}

// unattached stores an attachment with its two files, created at created, that
// no item points at.
func (h *harness) unattached(created time.Time) images.Attachment {
	h.t.Helper()
	id := uuid.New()
	a := images.Attachment{
		ID: id, StorageKey: "images/aa/bb/" + id.String() + ".jpg", ThumbnailStorageKey: "images/aa/bb/" + id.String() + ".thumb.jpg",
		MediaType: "image/jpeg", SizeBytes: 5, Width: 40, Height: 30, SHA256: make([]byte, 32),
	}
	h.putFile(a.StorageKey)
	h.putFile(a.ThumbnailStorageKey)
	h.store.cur.insert(a, created)
	return a
}

// attach is unattached, then points item at the attachment (version bumped as an upload would).
func (h *harness) attach(item uuid.UUID, created time.Time) images.Attachment {
	h.t.Helper()
	a := h.unattached(created)
	it := h.store.cur.items[item]
	it.Version++
	it.AttachmentID = &a.ID
	h.store.cur.items[item] = it
	return a
}

// fileKeys lists every file under images/.
func (h *harness) fileKeys() []string {
	h.t.Helper()
	var keys []string
	if err := h.files.Walk("images", func(key string, _ time.Time) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
	return keys
}

// exists reports whether key is a stored file.
func (h *harness) exists(key string) bool {
	h.t.Helper()
	body, _, err := h.files.Open(key)
	if errors.Is(err, storage.ErrNotFound) {
		return false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	_ = body.Close()
	return true
}

func upload() *images.Processed {
	return &images.Processed{
		Original: []byte("original bytes"), MediaType: "image/jpeg", Ext: "jpg",
		Width: 40, Height: 30, SHA256: [32]byte{7}, Thumbnail: []byte("thumb bytes"),
	}
}

// onlyAttachment returns the single attachment row of the fake database.
func (h *harness) onlyAttachment() images.Attachment {
	h.t.Helper()
	if len(h.store.cur.attachments) != 1 {
		h.t.Fatalf("attachments = %d, want 1", len(h.store.cur.attachments))
	}
	for _, a := range h.store.cur.attachments {
		return a
	}
	return images.Attachment{}
}

func TestSetUploadStaleVersionOrMissingItemLeavesNoFiles(t *testing.T) {
	tests := []struct {
		name    string
		version int
		missing bool
		check   func(error) bool
	}{
		{"stale If-Match", 1, false, func(err error) bool {
			var vc *apperr.VersionConflictError
			return errors.As(err, &vc) && vc.Current == 2
		}},
		{"unknown item", 2, true, func(err error) bool {
			var nf *apperr.NotFoundError
			return errors.As(err, &nf)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			item := h.addItem(2)
			if tt.missing {
				item = uuid.New()
			}
			_, err := h.svc.SetUpload(h.ctx, item, tt.version, upload(), nil)
			if !tt.check(err) {
				t.Fatalf("err = %v", err)
			}
			if keys := h.fileKeys(); len(keys) != 0 {
				t.Errorf("files left after a rejected upload: %v", keys)
			}
			if len(h.store.cur.attachments) != 0 {
				t.Errorf("attachment rows left after a rejected upload")
			}
		})
	}
}

// The previous files go only after the commit: when the commit fails, the old
// image stays intact and the new files are discarded.
func TestSetUploadDeletesPreviousFilesOnlyAfterCommit(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	old := h.attach(item, h.clock.now)
	version := h.store.cur.items[item].Version

	h.store.commitErr = errors.New("connection reset")
	if _, err := h.svc.SetUpload(h.ctx, item, version, upload(), nil); err == nil {
		t.Fatal("SetUpload succeeded despite a failed commit")
	}
	if !h.exists(old.StorageKey) || !h.exists(old.ThumbnailStorageKey) {
		t.Error("previous files deleted although the commit failed")
	}
	if got := len(h.fileKeys()); got != 2 {
		t.Errorf("files after a failed commit = %d, want 2 (the previous image only)", got)
	}

	h.store.commitErr = nil
	if _, err := h.svc.SetUpload(h.ctx, item, version, upload(), nil); err != nil {
		t.Fatal(err)
	}
	if h.exists(old.StorageKey) || h.exists(old.ThumbnailStorageKey) {
		t.Error("previous files still on disk after the commit")
	}
	if got := len(h.fileKeys()); got != 2 {
		t.Errorf("files after a replacement = %d, want 2", got)
	}
	if _, ok := h.store.cur.attachments[old.ID]; ok {
		t.Error("previous attachment row kept")
	}
}

// A failed delete after the commit is only logged: the upload succeeds and the
// old files stay as orphans for the sweeper.
func TestSetUploadFailedDeleteIsOnlyLogged(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	old := h.attach(item, h.clock.now)
	version := h.store.cur.items[item].Version

	h.files.failDelete = true
	if _, err := h.svc.SetUpload(h.ctx, item, version, upload(), nil); err != nil {
		t.Fatalf("SetUpload err = %v, want nil", err)
	}
	if !h.exists(old.StorageKey) {
		t.Error("old file gone although its delete failed")
	}
	if _, ok := h.store.cur.attachments[old.ID]; ok {
		t.Error("previous attachment row kept after a committed replacement")
	}
}

func TestSetUploadStoresCleanFilename(t *testing.T) {
	long := strings.Repeat("é", 300)
	tests := []struct {
		name string
		in   *string
		want *string
	}{
		{"nil", nil, nil},
		{"base name only", ptr("../../etc/holiday.jpg"), ptr("holiday.jpg")},
		{"windows separators", ptr(`C:\Users\me\cat.png`), ptr("cat.png")},
		{"control characters removed", ptr("a\x01b\x7fc.jpg"), ptr("abc.jpg")},
		{"trimmed", ptr("  pic.jpg \t"), ptr("pic.jpg")},
		{"nothing left", ptr("   "), nil},
		{"truncated to 255 characters", &long, ptr(strings.Repeat("é", 255))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			item := h.addItem(0)
			if _, err := h.svc.SetUpload(h.ctx, item, 0, upload(), tt.in); err != nil {
				t.Fatal(err)
			}
			got := h.onlyAttachment().OriginalFilename
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("OriginalFilename = %v, want %v", deref(got), deref(tt.want))
			}
		})
	}
}

func TestSetURLRules(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantURL string // empty when the URL must be rejected
	}{
		{"https", "https://example.com/cover.jpg", "https://example.com/cover.jpg"},
		{"http", "http://example.com/cover.jpg", "http://example.com/cover.jpg"},
		{"trimmed", "  https://example.com/x.png \n", "https://example.com/x.png"},
		{"other scheme", "ftp://example.com/x.png", ""},
		{"relative", "/covers/x.png", ""},
		{"no host", "https:///x.png", ""},
		{"not a URL", "not a url", ""},
		{"too long", "https://example.com/" + strings.Repeat("a", 2048), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			item := h.addItem(0)
			_, err := h.svc.SetURL(h.ctx, item, 0, tt.in)
			if tt.wantURL == "" {
				var v *apperr.ValidationError
				if !errors.As(err, &v) || v.Errors[0].Field != "url" {
					t.Fatalf("err = %v, want a ValidationError on url", err)
				}
				if h.store.cur.items[item].URL != nil {
					t.Error("rejected URL was stored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if u := h.store.cur.items[item].URL; u == nil || *u != tt.wantURL {
				t.Errorf("stored URL = %v, want %s", deref(u), tt.wantURL)
			}
		})
	}
}

func TestSetURLSameURLDoesNotBump(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(0)
	if _, err := h.svc.SetURL(h.ctx, item, 0, "https://example.com/a.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetURL(h.ctx, item, 1, "https://example.com/a.jpg"); err != nil {
		t.Fatal(err)
	}
	if v := h.store.cur.items[item].Version; v != 1 {
		t.Errorf("version = %d after a repeated URL, want 1", v)
	}
}

func TestSetURLReplacesAttachmentAfterCommit(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	old := h.attach(item, h.clock.now)

	if _, err := h.svc.SetURL(h.ctx, item, 2, "https://example.com/a.jpg"); err != nil {
		t.Fatal(err)
	}
	if h.exists(old.StorageKey) || h.exists(old.ThumbnailStorageKey) {
		t.Error("attachment files kept after the item switched to a URL")
	}
	if _, ok := h.store.cur.attachments[old.ID]; ok {
		t.Error("attachment row kept after the item switched to a URL")
	}
}

func TestDeleteRemovesImageAndRepeatIsNoop(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	old := h.attach(item, h.clock.now) // version 2

	if _, err := h.svc.Delete(h.ctx, item, 2); err != nil {
		t.Fatal(err)
	}
	if v := h.store.cur.items[item].Version; v != 3 {
		t.Errorf("version after delete = %d, want 3", v)
	}
	if h.exists(old.StorageKey) || len(h.store.cur.attachments) != 0 {
		t.Error("image not removed")
	}

	if _, err := h.svc.Delete(h.ctx, item, 3); err != nil {
		t.Fatal(err)
	}
	if v := h.store.cur.items[item].Version; v != 3 {
		t.Errorf("version after a repeated delete = %d, want 3 (no bump)", v)
	}
}

func TestOpenServesOriginalAndThumbnail(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	a := h.attach(item, h.clock.now)

	orig, err := h.svc.Open(h.ctx, a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orig.Body.Close() }()
	if orig.MediaType != "image/jpeg" || orig.Size != int64(len("bytes of "+a.StorageKey)) {
		t.Errorf("original = %s %d", orig.MediaType, orig.Size)
	}

	thumb, err := h.svc.Open(h.ctx, a.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = thumb.Body.Close() }()
	if thumb.MediaType != "image/jpeg" || thumb.Size != int64(len("bytes of "+a.ThumbnailStorageKey)) {
		t.Errorf("thumbnail = %s %d", thumb.MediaType, thumb.Size)
	}
}

func TestOpenMissingAttachmentIsNotFound(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	gone := h.attach(item, h.clock.now)
	if err := h.files.FileStore.Delete(gone.StorageKey); err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]uuid.UUID{"unknown id": uuid.New(), "file missing": gone.ID} {
		t.Run(name, func(t *testing.T) {
			var nf *apperr.NotFoundError
			if _, err := h.svc.Open(h.ctx, id, false); !errors.As(err, &nf) {
				t.Errorf("err = %v, want NotFound", err)
			}
		})
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
