# images

## Purpose

Owns item pictures on Huma: sets an item's image from an upload (multipart) or an external URL (JSON), removes it, serves stored files and sweeps orphans. Pure rules in `process.go` and `strip.go` (magic-byte sniffing, metadata stripping, thumbnails). Storage goes through `storage`. Design context: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §5, ARCHITECTURE §4 and §5).

## Key files

- `internal/images/handler.go`: Huma operations, `ifMatchFirst`, upload guard, multipart and JSON decoding, `StreamResponse` for files
- `internal/images/service.go`: `Service`, write order (files, tx, delete old files), `Open`
- `internal/images/repository.go`: `Postgres`, `Repo` and `Store`; item-image writes are `// SEAM` methods that move to items
- `internal/images/sweep.go`: `Sweep`, `RunSweeper`, workspace check, orphan age
- `internal/images/rules.go`, `model.go`: storage keys, display name and URL rules; `Attachment`, `ItemImageState`
- `internal/images/process.go`, `strip.go`: `Process`, `Strip` (pure)
- `db/queries/attachments.sql`: attachment queries

## Interfaces

- `PUT /api/v1/items/{id}/image`: `If-Match` required; `multipart/form-data` (`file` part) or `application/json` `{url}`; returns the item
- `DELETE /api/v1/items/{id}/image`: `If-Match` required; returns the item
- `GET /api/v1/attachments/{id}/file` and `/thumbnail`: streamed, immutable cache
- `Register(api, log, svc)`, `NewService(store, files, items, clk, log)`, `NewPostgres(db)`
- `(*Service)`: `SetUpload`, `SetURL`, `Delete`, `Open(ctx, id, thumbnail)`, `Sweep(ctx)`, `RunSweeper(ctx, interval)`
- Limits: `MaxUploadBytes` (10 MiB), `MaxPixels` (40 000 000), `ThumbnailMaxSide` (640), `SweepInterval` (24 h), orphan age 1 h

## Gotchas

- Writes: files before the tx, previous attachment row deleted inside it, previous files deleted after commit. A failed tx removes the new files only if no attachment row exists.
- Sweep refuses a context without a workspace (`scope.With`); `cmd/api` runs the sweeper with it. Without a workspace the file walk would treat every file as an orphan.
- Item-image writes (`LockItem`, `SetItemImage*`, `ClearItemImage`) are SEAM queries on items and move there later.
- Upload bodies are capped at 10 MiB plus the 64 KiB envelope; JSON bodies at 64 KiB. Bad media types answer 400, a missing `file` part 422.
- Every replacement gets a new attachment id; a served URL never changes content. Trashed items keep serving files.
- The stored original is stripped (`Strip`), so its SHA-256 differs from the upload. The thumbnail is re-encoded and has no metadata.
- JPEG keeps only an Orientation EXIF when it is not 1; PNG and WebP orientation is ignored.
- `SetURL` trims the URL (http/https, max 2048 characters) and changes nothing when it is unchanged (no version bump). `Delete` without an image also does not bump.
- Upload, URL set and delete set the item's `updated_by_user_id` to the caller (the no-op cases do not write).
- A row whose file is missing answers 404 and logs at ERROR. Failed file deletes are logged and left to the sweeper.
- Filenames are display-only: base name, control characters removed, max 255 characters.
