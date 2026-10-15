# images

## Purpose

Owns item pictures: validates uploads by magic bytes, strips embedded metadata from the stored original, builds a JPEG thumbnail, stores files through `storage`, sets or clears an item's image (upload or external URL), serves stored files and sweeps orphans. Also backfills metadata stripping for originals stored earlier. No HTTP; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §5, ARCHITECTURE §4 and §5).

## Key files

- `internal/images/process.go`: `Process`, sniffing, size and pixel limits, thumbnail, EXIF orientation
- `internal/images/strip.go`: `Strip`, lossless metadata removal for JPEG, PNG, WebP
- `internal/images/backfill.go`: `Backfill`, in-place strip of stored originals
- `internal/images/service.go`: `Service`, `SetUpload`, `SetURL`, `Delete`, `Open`, file ordering
- `internal/images/sweeper.go`: `Sweep`, `RunSweeper`, orphan age and interval
- `cmd/api/stripimages.go`: `strip-images` subcommand; `db/queries/attachments.sql`: attachment queries

## Interfaces

- `Process(r io.Reader) (*Processed, error)`: JPEG, PNG or WebP only; 413, 415 or 422 on failure
- `Strip(data []byte, mediaType string) ([]byte, error)`: removes EXIF, GPS, XMP, IPTC, comments; keeps ICC, JFIF, Adobe APP14
- `Backfill(ctx, db, store, log, workspaceID) (BackfillResult, error)`: strips stored originals, updates size and sha256
- `/app/api strip-images` (`make strip-images`): runs `Backfill` and prints counts; non-zero exit on I/O or DB errors
- `New(db, store, clk, items *items.Service, log, workspaceID) *Service`
- `(*Service).SetUpload`, `SetURL`, `Delete`, `Open(ctx, attachmentID, thumbnail bool)`, `Sweep`, `RunSweeper(ctx, interval)`
- Limits: `MaxUploadBytes` (10 MiB), `MaxPixels` (40 000 000), `ThumbnailMaxSide` (640), `SweepInterval` (24h)

## Gotchas

- Type comes from magic bytes only (SVG and anything else is 415). Dimensions are checked with `DecodeConfig` before the full decode.
- The stored original is the stripped copy; SHA256 is over the stripped bytes. The thumbnail is re-encoded from pixels, so it has no metadata either.
- Strip is lossless and strict: any malformed layout is an error. Bytes after EOI, IEND or the RIFF end are copied untouched.
- JPEG keeps a minimal EXIF segment with only Orientation when it is not 1. PNG and WebP orientation is ignored.
- Backfill never deletes, skips unparseable or oversize files, and refuses a strip whose `DecodeConfig` differs from the original. It is idempotent and writes via temp+rename.
- Writes: files first, then one tx (If-Match check, insert, point item, delete previous row), then delete previous files. A failed tx deletes the new files only if no attachment row exists.
- Every replacement gets a new attachment id, so a served URL never changes content.
- `SetURL` trims the URL, accepts http and https up to 2048 chars, and does not bump the version when the URL is unchanged. `Delete` without an image also does not bump.
- Trashed items keep serving their files. A row whose file is missing answers 404 and logs at ERROR.
- Sweeper: unreferenced attachment rows and unreferenced `images/` files older than 1 hour are removed; failures are retried next run. Logs carry counts only.
- Filenames are display-only: base name, control characters removed, max 255 characters.
