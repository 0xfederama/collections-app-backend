package images_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/images"
)

// An unreferenced attachment (its item was purged) is removed with its files
// once it is older than the hour; a younger one and any referenced one stay.
func TestSweepRemovesOldUnreferencedAttachments(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.now
	kept := h.attach(h.addItem(0), t0)
	orphan := h.unattached(t0)

	h.clock.now = t0.Add(30 * time.Minute)
	young := h.unattached(h.clock.now)

	h.clock.now = t0.Add(75 * time.Minute) // orphan is 75 min old, young is 45 min old
	res, err := h.svc.Sweep(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attachments != 1 || res.Files != 0 {
		t.Errorf("report = %+v, want 1 attachment and 0 orphan files", res)
	}
	if h.exists(orphan.StorageKey) || h.exists(orphan.ThumbnailStorageKey) {
		t.Error("files of the old unreferenced attachment kept")
	}
	if _, ok := h.store.cur.attachments[orphan.ID]; ok {
		t.Error("old unreferenced attachment row kept")
	}
	if !h.exists(young.StorageKey) {
		t.Error("young unreferenced attachment removed before the grace period")
	}
	if !h.exists(kept.StorageKey) {
		t.Error("file of a referenced attachment removed")
	}
}

// A file no row names is an orphan: it stays for the grace period and is then
// removed. Referenced files are never touched.
func TestSweepRemovesOrphanFilesAfterGrace(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(0)
	kept := h.attach(item, h.clock.now)
	h.putFile("images/cc/dd/crashed-upload.jpg")

	res, err := h.svc.Sweep(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 0 || !h.exists("images/cc/dd/crashed-upload.jpg") {
		t.Fatalf("young orphan file removed: report %+v", res)
	}

	h.clock.now = h.clock.now.Add(2 * time.Hour)
	res, err = h.svc.Sweep(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, want 1", res.Files)
	}
	if h.exists("images/cc/dd/crashed-upload.jpg") {
		t.Error("old orphan file kept")
	}
	if !h.exists(kept.StorageKey) || !h.exists(kept.ThumbnailStorageKey) {
		t.Error("files of a referenced attachment removed")
	}
}

// A replaced image whose file delete failed is an orphan; the next run after
// the grace period removes it.
func TestSweepRemovesFilesThatFailedToDeleteEarlier(t *testing.T) {
	h := newHarness(t)
	item := h.addItem(1)
	old := h.attach(item, h.clock.now)
	version := h.store.cur.items[item].Version

	h.files.failDelete = true
	if _, err := h.svc.SetUpload(h.ctx, item, version, upload(), nil); err != nil {
		t.Fatal(err)
	}
	if !h.exists(old.StorageKey) {
		t.Fatal("old file deleted although delete failed")
	}

	h.clock.now = h.clock.now.Add(2 * time.Hour)
	if _, err := h.svc.Sweep(h.ctx); err == nil {
		t.Fatal("Sweep returned no error although the orphan could not be deleted")
	}
	if !h.exists(old.StorageKey) {
		t.Fatal("orphan removed although delete still fails")
	}

	h.files.failDelete = false
	res, err := h.svc.Sweep(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 {
		t.Errorf("Files = %d, want 2 (the old original and thumbnail)", res.Files)
	}
	if h.exists(old.StorageKey) || h.exists(old.ThumbnailStorageKey) {
		t.Error("orphan files still on disk after the retry")
	}
	if got := len(h.fileKeys()); got != 2 {
		t.Errorf("files after the retry = %d, want 2 (the current image)", got)
	}
}

// Without a workspace the store would list nothing, so the walk would take every
// file for an orphan. Sweep refuses first: the old orphan file, the old
// unreferenced attachment and its row all survive.
func TestSweepRefusesUnscopedContext(t *testing.T) {
	h := newHarness(t)
	const orphan = "images/cc/dd/crashed-upload.jpg"
	h.putFile(orphan)
	stale := h.unattached(h.clock.now)
	h.clock.now = h.clock.now.Add(2 * time.Hour)

	res, err := h.svc.Sweep(context.Background())
	if err == nil {
		t.Fatal("Sweep with an unscoped context returned no error")
	}
	if res != (images.SweepResult{}) {
		t.Errorf("report = %+v, want zero", res)
	}
	if !h.exists(orphan) {
		t.Error("orphan file deleted by an unscoped sweep")
	}
	if !h.exists(stale.StorageKey) || !h.exists(stale.ThumbnailStorageKey) {
		t.Error("files of an unreferenced attachment deleted by an unscoped sweep")
	}
	if _, ok := h.store.cur.attachments[stale.ID]; !ok {
		t.Error("attachment row deleted by an unscoped sweep")
	}
}

// RunSweeper logs the refusal at ERROR and deletes nothing. The context is
// cancelled up front so the single run before the first tick is the only one.
func TestRunSweeperLogsUnscopedContextAndDeletesNothing(t *testing.T) {
	h := newHarness(t)
	const orphan = "images/cc/dd/crashed-upload.jpg"
	h.putFile(orphan)
	h.clock.now = h.clock.now.Add(2 * time.Hour)

	var logs bytes.Buffer
	svc := images.NewService(h.store, h.files, fakeItems{}, h.clock, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.RunSweeper(ctx, time.Hour)

	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no ERROR logged for an unscoped sweeper run: %q", logs.String())
	}
	if !h.exists(orphan) {
		t.Error("orphan file deleted by an unscoped sweeper run")
	}
}

func TestRunSweeperReturnsWhenCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		h.svc.RunSweeper(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSweeper did not return after cancel")
	}
}
