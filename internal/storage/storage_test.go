package storage_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/storage"
)

func newStore(t *testing.T) (*storage.Local, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestPutOpenDelete(t *testing.T) {
	s, _ := newStore(t)
	const key = "images/ab/cd/file.png"
	n, err := s.Put(key, strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Put = %d, %v", n, err)
	}
	f, size, err := s.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "hello" || size != 5 {
		t.Fatalf("read %q (%d)", b, size)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("deleting a missing file must be a no-op: %v", err)
	}
	if _, _, err := s.Open(key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open after delete = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestPutFailureLeavesNothing(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.Put("images/aa/bb/x.png", failingReader{}); err == nil {
		t.Fatal("want an error")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("leftover files: %v", files)
	}
}

func TestKeysCannotEscapeTheRoot(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../outside.txt", "images/../../outside.txt", "/etc/passwd", "images//a", "images/./a", "images/a\\b", "images/a b", "images/..", "images/"} {
		if _, err := s.Put(key, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q) = %v", key, err)
		}
		if _, _, err := s.Open(key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Open(%q) = %v", key, err)
		}
		if err := s.Delete(key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Delete(%q) = %v", key, err)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("a file outside the root was touched")
	}
}

func TestSymlinkOutsideRootIsRefused(t *testing.T) {
	s, dir := newStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "images")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, _, err := s.Open("images/f"); err == nil {
		t.Fatal("followed a symlink out of the root")
	}
}

func TestWalk(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Walk("images", func(string, time.Time) error { t.Fatal("empty store must not call fn"); return nil }); err != nil {
		t.Fatalf("missing prefix: %v", err)
	}
	for _, k := range []string{"images/aa/bb/one.png", "images/cc/dd/two.thumb.jpg"} {
		if _, err := s.Put(k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "images/aa/bb/one.png"), old, old); err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	if err := s.Walk("images", func(k string, m time.Time) error { got[k] = m; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["images/aa/bb/one.png"].Before(time.Now().Add(-24*time.Hour)) {
		t.Fatalf("walk = %v", got)
	}
}
