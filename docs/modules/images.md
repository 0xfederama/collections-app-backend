# images

## Purpose

Owns item pictures: validates uploads by magic bytes, builds a JPEG thumbnail, stores files through `storage`, sets or clears an item's image (upload or external URL), serves stored files and sweeps orphans. No HTTP; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §5, ARCHITECTURE §4 and §5).

## Key files

- `internal/images/process.go`: `Process`, sniffing, size and pixel limits, thumbnail, EXIF orientation
- `internal/images/service.go`: `Service`, `SetUpload`, `SetURL`, `Delete`, `Open`, file ordering
- `internal/images/sweeper.go`: `Sweep`, `RunSweeper`, orphan age and interval
- `db/queries/attachments.sql`: attachment and item image queries
- `internal/images/process_test.go`, `internal/httpapi/images_test.go`: tests

## Interfaces

- `Process(r io.Reader) (*Processed, error)`: JPEG, PNG or WebP only; 413, 415 or 422 on failure
- `New(db, store, clk, items *items.Service, log, workspaceID) *Service`
- `(*Service).SetUpload(ctx, itemID, version, *Processed, filename *string) (items.Item, error)`
- `(*Service).SetURL(ctx, itemID, version, rawURL) (items.Item, error)`
- `(*Service).Delete(ctx, itemID, version) (items.Item, error)`
- `(*Service).Open(ctx, attachmentID, thumbnail bool) (File, error)`: `File` has `Body`, `MediaType`, `Size`
- `(*Service).Sweep(ctx) (SweepResult, error)`; `RunSweeper(ctx, interval)` runs at start and then per interval
- Limits: `MaxUploadBytes` (10 MiB), `MaxPixels` (40 000 000), `ThumbnailMaxSide` (640), `SweepInterval` (24h)

## Gotchas

- Type comes from magic bytes only (SVG and anything else is 415). Dimensions are checked with `DecodeConfig` before the full decode.
- The thumbnail is re-encoded from pixels, so metadata is dropped there. The stored original is byte-for-byte the upload.
- EXIF orientation is applied only to JPEGs (thumbnail and stored width/height). PNG and WebP orientation is ignored.
- Writes: files first, then one tx (If-Match check, insert, point item, delete previous row), then delete previous files. A failed tx deletes the new files only if no attachment row exists.
- Every replacement gets a new attachment id, so a served URL never changes content.
- `SetURL` trims the URL, accepts http and https up to 2048 chars, and does not bump the version when the URL is unchanged. `Delete` without an image also does not bump.
- Trashed items keep serving their files. A row whose file is missing answers 404 and logs at ERROR.
- Sweeper: unreferenced attachment rows and unreferenced `images/` files older than 1 hour are removed; failures are retried next run. Logs carry counts only.
- Filenames are display-only: base name, control characters removed, max 255 characters.
