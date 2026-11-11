# trash

## Purpose

Moves folders, lists and items to the trash as one batch per delete, lists the batches, restores them and purges them for good (one batch, the whole trash, or batches past `trash_retention_days` via a daily job). Every delete, restore and purge takes the tree lock first. Huma operations; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §4, API.md §5).

## Key files

- `internal/trash/manager.go`: `Service`, delete, list and restore rules
- `internal/trash/purge.go`: `Purge`, `PurgeAll`, shared batch purge, file deletes after commit
- `internal/trash/walk.go`: pure folder walks in Go (subtree, active subtree, ancestors, cycle check)
- `internal/trash/repository.go`: `Repo` and `Store` interfaces, `Postgres` plain queries; SEAM methods touch other features' tables
- `internal/trash/model.go`: `Entry`, `Page`, `Destination`, `Batch`, `FolderNode`
- `internal/trash/autopurge.go`: `PurgeExpired`, `RunAutoPurge`
- `internal/trash/handler.go`, `wire.go`: Huma operations and wire types
- `db/queries/trash.sql`: batch, row and purge queries (no recursive CTE)

## Interfaces

- `NewService(store Store, files storage.FileStore, clk clock.Clock, log *slog.Logger) *Service`; `NewPostgres(db DB)` is the `Store`
- `(*Service).DeleteFolder` / `DeleteList` / `DeleteItem(ctx, id, version) error`: stale version is a conflict
- `(*Service).List(ctx, limit, offset) (Page, error)`: newest first, counts include the root
- `(*Service).Restore(ctx, batchID, *Destination) (Restored, error)`: `nil` destination = original place
- `(*Service).Purge(ctx, batchID)`, `PurgeAll(ctx) (int, error)`, `PurgeExpired(ctx) (int, error)`
- `WithAccess(a Access) *Service`: the trash list, `PurgeAll` and restore destinations are limited to the caller's boxes ([access](access.md)); auto-purge uses the service without it
- `(*Service).RunAutoPurge(ctx, interval)`: `PurgeExpired` at start and every interval; `cmd/api` runs it every 24h
- `Register(api, log, svc)`: `deleteFolder`, `deleteList`, `deleteItem` (If-Match), `listTrash`, `restoreTrashBatch`, `purgeTrashBatch`, `emptyTrash`

## Gotchas

- Delete marks the root and every active descendant folder, the lists in them and their items with one `trash_batch_id`. The descent stops at a trashed folder; older nested batches keep their batch.
- The former path is the active ancestors. The batch id, not the root id, is the path parameter for restore and purge.
- Batch visibility follows the root's box, trashed roots included. The `{batch_id}` guard answers 404 for another user's batch.
- Restore without a destination answers 409 `parent_in_trash` (with `blocking_parent`) when the original parent is not active.
- With a destination, `cycle_detected` (any folder state) is checked before `destination_unavailable`. A destination the caller cannot see is `destination_unavailable`, checked before the transaction.
- Restore order: root first (appended last, version bumped), then the rest of the batch, then the batch row (D-051).
- Purge collects the subtree (folders, lists and items in any state, any batch) and deletes children first; nested batches whose root is gone go too. Image files are deleted after commit; a failure is logged and the images sweeper retries. The `user_top_order` rows of the purged roots are deleted explicitly (no foreign key, D-118).
- `PurgeAll` and `PurgeExpired` share `purgeBatches` with `Purge`: same rules, one tx under the tree lock.
- Auto-purge is off unless `trash_retention_days` is set; batches are cut by `deleted_at`. A failed run is logged and retried on the next tick. Its context must carry the workspace (`scope.With`).
- Every entry point refuses a context without a workspace (`errUnscopedTrash`); `RunAutoPurge` logs it per tick. Run jobs under `scope.With`.
