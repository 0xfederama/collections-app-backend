package images_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

type seeded struct {
	id        uuid.UUID
	key       string
	original  []byte // bytes put in the store
	wantClean []byte // nil: the file must stay as it is
}

func readKey(t *testing.T, store storage.FileStore, key string) []byte {
	t.Helper()
	rc, _, err := store.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func modTimes(t *testing.T, store storage.FileStore) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	if err := store.Walk("images", func(key string, mt time.Time) error { out[key] = mt; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestF005B1_BackfillStripsStoredOriginals(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	wsID, err := workspace.Bootstrap(ctx, pool, "test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base := jpegBytes(t, 40, 30)
	dirtyPng, cleanPng := dirtyPNG(t)
	cleanJpg := jpegBytes(t, 20, 20)
	garbage := append([]byte{0xFF, 0xD8, 0xFF, 0xE1}, bytes.Repeat([]byte{7}, 40)...)
	webpDirty := riffFile(vp8x(0x0C), vp8l(), riffChunk("EXIF", []byte("Exif\x00\x00")), riffChunk("XMP ", []byte("<x/>")))

	type spec struct {
		name, mediaType, ext string
		data                 []byte
		clean                []byte // expected stored bytes after the run; nil = untouched
		typ                  string // stripped, clean, stale, skipped, missing
	}
	specs := []spec{
		{"dirty jpeg", "image/jpeg", "jpg", dirtyJPEG(base, 6), nil, "stripped"},
		{"dirty png", "image/png", "png", dirtyPng, cleanPng, "stripped"},
		{"dirty webp", "image/webp", "webp", webpDirty, riffFile(vp8x(0), vp8l()), "stripped"},
		{"clean jpeg", "image/jpeg", "jpg", cleanJpg, nil, "clean"},
		{"clean with stale row", "image/jpeg", "jpg", base, nil, "stale"},
		{"corrupt", "image/jpeg", "jpg", garbage, nil, "skipped"},
		{"missing", "image/png", "png", nil, nil, "missing"},
	}
	// The expected result of the dirty JPEG is whatever Strip makes of it.
	if specs[0].clean, err = images.Strip(specs[0].data, "image/jpeg"); err != nil {
		t.Fatal(err)
	}

	q := sqlc.New(pool)
	rows := make([]seeded, len(specs))
	for i, sp := range specs {
		id := uuid.New()
		key := "images/" + id.String()[:2] + "/" + id.String()[2:4] + "/" + id.String() + "." + sp.ext
		sum := sha(sp.data)
		size := int64(len(sp.data))
		if sp.typ == "stale" {
			sum, size = sha([]byte("old")), 3
		}
		if sp.typ == "missing" {
			size = 1
		} else if _, err := store.Put(key, bytes.NewReader(sp.data)); err != nil {
			t.Fatal(err)
		}
		if err := q.InsertAttachment(ctx, sqlc.InsertAttachmentParams{
			ID: id, WorkspaceID: wsID, StorageKey: key, ThumbnailStorageKey: key + ".thumb.jpg", MediaType: sp.mediaType,
			SizeBytes: size, Width: 1, Height: 1, Sha256: sum[:], Now: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		rows[i] = seeded{id: id, key: key, original: sp.data, wantClean: sp.clean}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res, err := images.Backfill(ctx, pool, store, log, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (images.BackfillResult{Total: 7, Stripped: 3, AlreadyClean: 2, Skipped: 2}); res != want {
		t.Fatalf("first run = %+v, want %+v", res, want)
	}

	for i, sp := range specs {
		r := rows[i]
		var size int64
		var sum []byte
		var width int32
		err := pool.QueryRow(ctx, "SELECT size_bytes, sha256, width FROM attachments WHERE id = $1", r.id).Scan(&size, &sum, &width)
		if err != nil {
			t.Fatal(err)
		}
		if width != 1 {
			t.Errorf("%s: width changed", sp.name)
		}
		switch sp.typ {
		case "missing":
			if size != 1 {
				t.Errorf("%s: row must stay", sp.name)
			}
			continue
		case "skipped":
			if got := readKey(t, store, r.key); !bytes.Equal(got, r.original) {
				t.Errorf("%s: a file that cannot be parsed must stay untouched", sp.name)
			}
			continue
		}
		got := readKey(t, store, r.key)
		want := sp.clean
		if want == nil {
			want = sp.data
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: stored file is not the expected stripped file", sp.name)
		}
		for _, marker := range [][]byte{[]byte("Exif"), []byte("EXIF"), []byte("XMP "), []byte("tEXt"), []byte("GPS"), latitudeMarker} {
			if sp.typ == "stripped" && sp.name != "dirty jpeg" && bytes.Contains(got, marker) {
				t.Errorf("%s: still contains %q", sp.name, marker)
			}
		}
		if sp.name == "dirty jpeg" && (bytes.Contains(got, latitudeMarker) || images.JPEGOrientation(got) != 6) {
			t.Errorf("%s: GPS left or orientation lost", sp.name)
		}
		wantSum := sha(got)
		if size != int64(len(got)) || !bytes.Equal(sum, wantSum[:]) {
			t.Errorf("%s: row has size %d / sha %x, file has %d / %x", sp.name, size, sum, len(got), wantSum)
		}
	}

	// Second run: nothing to strip, no file is written again.
	filesBefore := map[string][]byte{}
	for _, r := range rows {
		if _, _, err := store.Open(r.key); err == nil {
			filesBefore[r.key] = readKey(t, store, r.key)
		}
	}
	timesBefore := modTimes(t, store)
	res, err = images.Backfill(ctx, pool, store, log, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (images.BackfillResult{Total: 7, AlreadyClean: 5, Skipped: 2}); res != want {
		t.Fatalf("second run = %+v, want %+v", res, want)
	}
	for key, b := range filesBefore {
		if !bytes.Equal(readKey(t, store, key), b) {
			t.Errorf("%s changed on the second run", key)
		}
	}
	for key, mt := range modTimes(t, store) {
		if !mt.Equal(timesBefore[key]) {
			t.Errorf("%s was rewritten on the second run", key)
		}
	}
}

func TestF005B1_BackfillReportsDatabaseErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	wsID, err := workspace.Bootstrap(ctx, pool, "test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := images.Backfill(ctx, pool, store, log, wsID); err == nil {
		t.Fatal("a cancelled context must be reported")
	}
}
