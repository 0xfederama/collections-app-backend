-- Trash (B-10, DATA_MODEL §4). Every statement runs inside a transaction that
-- holds the tree lock (see tree.sql). "Active" means deleted_at IS NULL.

-- name: InsertTrashBatch :one
INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path, deleted_at)
VALUES (@workspace_id, @root_type, @root_id, @root_name, @former_path, @now)
RETURNING id;

-- name: LockTrashBatch :one
SELECT * FROM trash_batches
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: GetTrashRetentionDays :one
-- The auto-purge setting (NULL = off). No row means no settings: the job treats it as off.
SELECT trash_retention_days FROM workspace_settings WHERE workspace_id = @workspace_id;

-- name: ListExpiredTrashBatchIDs :many
-- Batches trashed before the cutoff (deleted_at is the trash time). Read under the tree lock.
SELECT id FROM trash_batches WHERE workspace_id = @workspace_id AND deleted_at < @cutoff;

-- name: ListTrashBatchIDs :many
-- Every batch of the workspace (the whole trash, not a page). Read under the tree lock.
SELECT id FROM trash_batches WHERE workspace_id = @workspace_id;

-- name: DeleteTrashBatch :exec
DELETE FROM trash_batches WHERE id = @id AND workspace_id = @workspace_id;

-- name: DeleteOrphanTrashBatches :exec
-- Batches whose root row no longer exists (a purge removed it, directly or by
-- cascade). Nothing references them any more, since their rows were descendants of the root.
DELETE FROM trash_batches b
WHERE b.workspace_id = @workspace_id
  AND NOT EXISTS (SELECT 1 FROM folders f WHERE b.root_type = 'folder' AND f.id = b.root_id)
  AND NOT EXISTS (SELECT 1 FROM lists l WHERE b.root_type = 'list' AND l.id = b.root_id)
  AND NOT EXISTS (SELECT 1 FROM items i WHERE b.root_type = 'item' AND i.id = b.root_id);

-- name: ActiveFolderSubtreeIDs :many
-- The folder and its active descendant folders. UNION (not UNION ALL) so corrupt
-- data with a cycle still terminates.
WITH RECURSIVE sub(id) AS (
    SELECT f.id FROM folders f
    WHERE f.id = @root_id AND f.workspace_id = @workspace_id AND f.deleted_at IS NULL
  UNION
    SELECT c.id FROM folders c
    JOIN sub s ON c.parent_folder_id = s.id
    WHERE c.workspace_id = @workspace_id AND c.deleted_at IS NULL
)
SELECT id FROM sub;

-- name: TrashFolders :exec
UPDATE folders
SET deleted_at = @now, trash_batch_id = @batch_id, updated_at = @now
WHERE folders.workspace_id = @workspace_id AND folders.deleted_at IS NULL AND folders.id = ANY(@ids::uuid[]);

-- name: TrashListsInFolders :exec
UPDATE lists
SET deleted_at = @now, trash_batch_id = @batch_id, updated_at = @now
WHERE workspace_id = @workspace_id AND deleted_at IS NULL AND folder_id = ANY(@folder_ids::uuid[]);

-- name: TrashList :exec
UPDATE lists
SET deleted_at = @now, trash_batch_id = @batch_id, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: TrashItemsOfBatchLists :exec
-- Active items of every list this batch has just trashed.
UPDATE items
SET deleted_at = @now, trash_batch_id = @batch_id, updated_at = @now
WHERE items.workspace_id = @workspace_id AND items.deleted_at IS NULL
  AND items.list_id IN (SELECT l.id FROM lists l WHERE l.workspace_id = @workspace_id AND l.trash_batch_id = @batch_id);

-- name: TrashItem :exec
UPDATE items
SET deleted_at = @now, trash_batch_id = @batch_id, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ListTrashBatches :many
-- Newest first. The counts include the root. restorable_in_place is true when
-- the root's original parent is the workspace root or still active.
SELECT b.id, b.root_type, b.root_id, b.root_name, b.former_path, b.deleted_at,
       (SELECT count(*) FROM folders f WHERE f.trash_batch_id = b.id)::int AS folder_count,
       (SELECT count(*) FROM lists l WHERE l.trash_batch_id = b.id)::int AS list_count,
       (SELECT count(*) FROM items i WHERE i.trash_batch_id = b.id)::int AS item_count,
       EXISTS (
         SELECT 1 FROM (
             SELECT f.parent_folder_id AS pid, 'folder'::text AS ptype FROM folders f WHERE b.root_type = 'folder' AND f.id = b.root_id
           UNION ALL
             SELECT l.folder_id, 'folder'::text FROM lists l WHERE b.root_type = 'list' AND l.id = b.root_id
           UNION ALL
             SELECT i.list_id, 'list'::text FROM items i WHERE b.root_type = 'item' AND i.id = b.root_id
         ) rp
         WHERE rp.pid IS NULL
            OR (rp.ptype = 'folder' AND EXISTS (SELECT 1 FROM folders pf WHERE pf.id = rp.pid AND pf.deleted_at IS NULL))
            OR (rp.ptype = 'list' AND EXISTS (SELECT 1 FROM lists pl WHERE pl.id = rp.pid AND pl.deleted_at IS NULL))
       ) AS restorable_in_place
FROM trash_batches b
WHERE b.workspace_id = @workspace_id
ORDER BY b.deleted_at DESC, b.id DESC
LIMIT @page_limit OFFSET @page_offset;

-- name: CountTrashBatches :one
SELECT count(*)::int AS total FROM trash_batches WHERE workspace_id = @workspace_id;

-- name: GetTrashedFolder :one
-- The batch's folder root, whatever its state (a restore reads it while trashed).
SELECT * FROM folders WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id FOR UPDATE;

-- name: GetTrashedList :one
SELECT * FROM lists WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id FOR UPDATE;

-- name: GetTrashedItem :one
SELECT * FROM items WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id FOR UPDATE;

-- name: GetAnyFolderState :one
-- Name and trashed flag of a folder, for the blocking parent of a restore.
SELECT name, (deleted_at IS NOT NULL)::bool AS trashed FROM folders WHERE id = @id AND workspace_id = @workspace_id;

-- name: GetAnyListState :one
SELECT name, (deleted_at IS NOT NULL)::bool AS trashed FROM lists WHERE id = @id AND workspace_id = @workspace_id;

-- name: FolderIsInSubtreeOf :one
-- Counts 1 when folder_id is the folder candidate_id itself or one of its
-- ancestors, whatever their state: the walk goes up from the candidate. A trash
-- restore uses it to tell "into its own subtree" (cycle) from "unavailable".
WITH RECURSIVE chain(id, parent_folder_id) AS (
    SELECT f.id, f.parent_folder_id FROM folders f
    WHERE f.id = @candidate_id AND f.workspace_id = @workspace_id
  UNION
    SELECT p.id, p.parent_folder_id FROM folders p
    JOIN chain c ON p.id = c.parent_folder_id
    WHERE p.workspace_id = @workspace_id
)
SELECT count(*)::int AS matches FROM chain WHERE chain.id = sqlc.arg(folder_id)::uuid;

-- name: RestoreFolderRoot :exec
UPDATE folders
SET parent_folder_id = sqlc.narg(parent_folder_id), sort_order = @sort_order, deleted_at = NULL, trash_batch_id = NULL,
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: RestoreListRoot :exec
UPDATE lists
SET folder_id = sqlc.narg(folder_id), sort_order = @sort_order, deleted_at = NULL, trash_batch_id = NULL,
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: RestoreItemRoot :exec
UPDATE items
SET list_id = @list_id, sort_order = @sort_order, deleted_at = NULL, trash_batch_id = NULL,
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: RestoreBatchFolders :exec
-- The rest of the batch (the root is already restored). Versions stay.
UPDATE folders SET deleted_at = NULL, trash_batch_id = NULL, updated_at = @now
WHERE workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: RestoreBatchLists :exec
UPDATE lists SET deleted_at = NULL, trash_batch_id = NULL, updated_at = @now
WHERE workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: RestoreBatchItems :exec
UPDATE items SET deleted_at = NULL, trash_batch_id = NULL, updated_at = @now
WHERE workspace_id = @workspace_id AND trash_batch_id = @batch_id;

-- name: ListFolderParents :many
-- Every folder of the workspace with its parent, trashed or not. The purge walks
-- the root's subtree over these rows in Go (cycle-safe). The purge queries below
-- read lists and items in any state, under any batch.
SELECT id, parent_folder_id FROM folders
WHERE workspace_id = @workspace_id;

-- name: ListListIDsByFolders :many
SELECT id FROM lists
WHERE workspace_id = @workspace_id AND folder_id = ANY(@folder_ids::uuid[]);

-- name: ListItemIDsByLists :many
SELECT id FROM items
WHERE workspace_id = @workspace_id AND list_id = ANY(@list_ids::uuid[]);

-- name: ListAttachmentKeysByItems :many
-- Image attachments of the items, with their file keys. Read before the items are deleted.
SELECT a.id, a.storage_key, a.thumbnail_storage_key
FROM attachments a
WHERE a.workspace_id = @workspace_id
  AND a.id IN (
    SELECT i.image_attachment_id FROM items i
    WHERE i.workspace_id = @workspace_id AND i.image_attachment_id IS NOT NULL AND i.id = ANY(@item_ids::uuid[])
  );

-- name: DeleteContentVersionsOfItems :exec
DELETE FROM item_content_versions WHERE item_id = ANY(@item_ids::uuid[]);

-- name: DeleteLookupImportsOfItems :exec
DELETE FROM lookup_imports WHERE workspace_id = @workspace_id AND item_id = ANY(@item_ids::uuid[]);

-- name: DeleteItems :exec
DELETE FROM items WHERE workspace_id = @workspace_id AND id = ANY(@item_ids::uuid[]);

-- name: DeleteLists :exec
DELETE FROM lists WHERE workspace_id = @workspace_id AND id = ANY(@list_ids::uuid[]);

-- name: DeleteFolders :exec
-- One statement for the whole subtree: the parent check runs at the end of the statement.
DELETE FROM folders WHERE workspace_id = @workspace_id AND id = ANY(@folder_ids::uuid[]);

-- name: PurgeAttachmentRows :exec
DELETE FROM attachments WHERE workspace_id = @workspace_id AND id = ANY(@ids::uuid[]);
