# trash

## Purpose

Moves folders, lists and items to the trash as one batch per delete, lists the batches, restores them and purges them for good (one batch, the whole trash, or the batches past the workspace's `trash_retention_days` via a daily job). Every delete, restore and purge takes the tree lock first. No HTTP; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §4, API.md §5).

## Key files

- `internal/trash/trash.go`: `Service`, delete, list, restore and purge logic
- `db/queries/trash.sql`: batch, subtree, restore and purge queries
- `internal/httpapi/trash.go`: DELETE and trash handlers, `Destination` mapping

## Interfaces

- `New(db DB, store storage.FileStore, clk clock.Clock, log *slog.Logger, workspaceID uuid.UUID) *Service`
- `(*Service).DeleteFolder` / `DeleteList` / `DeleteItem(ctx, id, version) error`: If-Match version checked here
- `(*Service).List(ctx, limit, offset) (Page, error)`: newest first, counts include the root, `RestorableInPlace`
- `(*Service).Restore(ctx, batchID, *Destination) (Restored, error)`: `nil` destination = original place
- `(*Service).Purge(ctx, batchID) error`
- `(*Service).PurgeAll(ctx) (int, error)`: every batch in one tx, returns the batches removed (F012-B2)
- `(*Service).PurgeExpired(ctx) (int, error)`: batches trashed before `now - trash_retention_days`; NULL retention purges nothing
- `(*Service).RunAutoPurge(ctx, interval)`: runs PurgeExpired at start and every interval until ctx ends; started from `cmd/api`, every 24h
- `Destination`: folder and list roots use `FolderSet` + `FolderID` (nil = workspace root); item roots use `ListID`
- `DELETE /api/v1/folders/{id}`, `DELETE /api/v1/lists/{id}`, `DELETE /api/v1/items/{id}`
- `GET /api/v1/trash`, `POST /api/v1/trash/{batch_id}/restore`, `DELETE /api/v1/trash/{batch_id}`, `DELETE /api/v1/trash` (empty trash, 204, no If-Match)

## Gotchas

- Delete marks the root and every currently active descendant with one `trash_batch_id`; the former path snapshots the active ancestors. Rows already in the trash keep their older batch.
- The batch id, not the root id, is the path parameter for restore and purge.
- Restore without a destination answers 409 `parent_in_trash` (with `blocking_parent`) when the original parent is not active; items check their list the same way.
- Restore with a destination validates like a move. `cycle_detected` (folder into its own subtree, any state) is checked before `destination_unavailable`.
- A restored root is appended last among its siblings and its version is bumped; the rest of the batch keeps its versions.
- Purge collects the subtree ids in Go (folders, their lists, their items) and deletes rows explicitly, children first (content versions, lookup imports, items, lists, folders, attachment rows, batches); no FK cascade. Batches inside the subtree go too. Image files are deleted after commit; a failed delete is logged and the images sweeper removes it later.
- Purge collects attachments of the subtree's items in any state and any batch.
- PurgeAll and PurgeExpired share `purgeBatch` with Purge: same rules, one tx, files deleted after commit.
- Auto-purge is off unless the workspace sets `trash_retention_days`; a failed run is logged and retried on the next tick. Batches are cut by `deleted_at` (trash time), not by the batch's content.
