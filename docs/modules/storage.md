# storage

## Purpose

Keeps binary files (image originals and thumbnails) on the local filesystem under `DATA_DIR`, addressed by generated keys. Every path goes through an `os.Root`, so keys cannot leave the data directory. Design context: `/Users/federico/Developer/collections-app/design-docs/` (ARCHITECTURE §4).

## Key files

- `internal/storage/storage.go`: `FileStore` interface, `Local` implementation, `validKey`
- `internal/storage/storage_test.go`: put/open/delete, failed put, key escape, symlink, walk tests

## Interfaces

- `NewLocal(dir string) (*Local, error)`: opens `dir` (must exist) as the root
- `(*Local).Close() error`: releases the root handle
- `FileStore.Put(key, r) (int64, error)`: temp file `.tmp-<uuid>` next to the key, fsync, then rename
- `FileStore.Open(key) (io.ReadCloser, int64, error)`: returns the file and its size; missing is `ErrNotFound`
- `FileStore.Delete(key) error`: missing file is not an error
- `FileStore.Walk(prefix, fn func(key, modTime) error) error`: every regular file under `prefix`; missing prefix is an empty walk
- `ErrNotFound`, `ErrInvalidKey`: sentinel errors

## Gotchas

- Keys must be clean, relative, slash-separated, with segments of `[A-Za-z0-9._-]`; anything else is `ErrInvalidKey`.
- `Walk` also returns leftover `.tmp-*` files from a crashed `Put`; the image sweeper removes them once they are an hour old.
- A failed `Put` removes its temp file; a successful one is visible only after the rename.
- Symlinks that point outside the root are refused by the `os.Root`.
- `internal/images` writes `images/<2 hex>/<2 hex>/<uuid>.<ext>` and `.thumb.jpg`; `internal/favicons` writes `favicons/<sha256 hex>.img` and `.json`. No user input reaches a key.
