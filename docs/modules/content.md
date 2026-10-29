# content

## Purpose

Owns the Markdown body of an item: the current content, saved versions, restore of an old version and retention pruning. Versions are an ETag stream of their own (`"c-N"`). No HTTP; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 item_content_versions, D-052).

## Key files

- `internal/content/content.go`: `Service`, `Save`, `Restore`, `write` (the one save path), `validateBody`
- `db/queries/content.sql`: sqlc inputs for locking, versions, pruning, `updated_at` touch
- `internal/httpapi/content.go`: content handlers and `contentBody` mapping
- `internal/content/content_test.go`: service tests
- `internal/platform/etag/etag.go`: `Content = "c"` prefix

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Get(ctx, itemID) (Content, error)`: current content of an active item
- `(*Service).Version(ctx, itemID, version) (Content, error)`: one saved version with its body
- `(*Service).Versions(ctx, itemID) ([]VersionInfo, error)`: version, size and time, newest first, no bodies
- `(*Service).Save(ctx, itemID, ifMatch, body) (Content, error)`: stores `body` as the next version
- `(*Service).Restore(ctx, itemID, ifMatch, version) (Content, error)`: saves an old body as a new version

## Gotchas

- The current version is the highest row; no rows means version 0 (body `""`, `updated_at` null).
- Save locks the item (`FOR NO KEY UPDATE`) first; a stale `ifMatch` is 412 with the current version.
- A body identical to the current one (sha256) returns the current version with no insert and no `updated_at` bump.
- A new version bumps `items.updated_at` but never `items.version`.
- Prune deletes versions `<= new - (limit + 1)` using the workspace `markdown_history_limit`. Limit 0 keeps only the latest; lowering the limit prunes on the next save of each item.
- Restore reads the old body inside the transaction; a pruned or unknown version is 404.
- Body over 1 MiB of UTF-8 bytes is 413; invalid UTF-8 or a NUL character is 422. The guard's transport limit (2 MiB) is checked before this.
- Trashed or missing items are 404 for every operation.
