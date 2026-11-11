# content

## Purpose

Owns the Markdown body of an item: the current content, saved versions, restore of an old version and retention pruning. Versions are an ETag stream of their own (`"c-N"`). No database access outside `repository.go`; returns `apperr` errors; HTTP is Huma. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 item_content_versions, D-052).

## Key files

- `internal/content/model.go`: `Content`, `VersionInfo`, `Stored`, `MaxBodyBytes`
- `internal/content/service.go`: `Service`, `Save`, `Restore`, `write` (the one save path), `validateBody`
- `internal/content/repository.go`: `Postgres` and per-transaction `repo`, the only sqlc user
- `internal/content/handler.go`, `wire.go`: Huma operations and JSON shapes
- `internal/content/content_test.go`: service tests
- `db/queries/content.sql`: locking, versions, pruning, `updated_at` touch

## Interfaces

- `NewService(store Store, settings SettingsSource, clk clock.Clock) *Service`; `NewPostgres(db DB) *Postgres`
- `(*Service).Get(ctx, itemID) (Content, error)`: current content of an active item
- `(*Service).Version(ctx, itemID, version) (Content, error)`: one saved version with its body
- `(*Service).Versions(ctx, itemID) ([]VersionInfo, error)`: version, size and time, newest first, no bodies
- `(*Service).Save(ctx, itemID, ifMatch, body) (Content, error)`; `Restore(ctx, itemID, ifMatch, version)`
- `Register(api, log, svc)`: `GET`/`PUT /api/v1/items/{id}/content`, `GET /api/v1/items/{id}/content/versions`, `GET .../versions/{version}`, `POST .../content/restore`

## Gotchas

- The current version is the highest row; no rows means version 0 (body `""`, `updated_at` null).
- Save locks the item (`FOR NO KEY UPDATE`) first; a stale `ifMatch` is 412 with the current version.
- The history limit is read before the transaction; the body checks (413, 422) run before that.
- A body identical to the current one (sha256) returns the current version with no insert and no `updated_at` bump.
- A new version bumps `items.updated_at` and `items.updated_by_user_id` but never `items.version`.
- `author_user_id` on each version (`Content`, version list) is the caller who saved it; null for version 0 and for versions saved before F-027 B4. Restore saves through the same path, so it records the caller too.
- Prune deletes versions `<= new - (limit + 1)` using the workspace `markdown_history_limit`. Limit 0 keeps only the latest; lowering the limit prunes on the next save of each item.
- Restore reads the old body inside the transaction; a pruned or unknown version is 404.
- Body over 1 MiB of UTF-8 bytes is 413; invalid UTF-8 or a NUL character is 422. The operation's transport limit (2 MiB, `contentBodyBytes`) applies first.
- The version list is unpaginated, newest first, with no SQL limit; its size is what pruning keeps (the operation text says at most 201).
- Trashed or missing items are 404 for every operation.
