-- Ownership and visibility (F-027 B2). Every query filters by workspace_id. The access feature
-- reads the whole folder and list structure in any state: it needs ancestors that are trashed.

-- name: AccessFolders :many
SELECT id, parent_folder_id, owner_user_id, (deleted_at IS NOT NULL)::bool AS trashed
FROM folders WHERE workspace_id = @workspace_id;

-- name: AccessLists :many
SELECT id, folder_id, owner_user_id, (deleted_at IS NOT NULL)::bool AS trashed
FROM lists WHERE workspace_id = @workspace_id;

-- name: AccessItemList :one
-- The list of an item in any state.
SELECT list_id FROM items WHERE id = @id AND workspace_id = @workspace_id;

-- name: AccessAttachmentItem :one
-- The item that uses an attachment, as its image or as its lookup poster. No row = an orphan.
SELECT item_id FROM (
    SELECT i.id AS item_id FROM items i
    WHERE i.workspace_id = @workspace_id AND i.image_attachment_id = @id
  UNION ALL
    SELECT li.item_id FROM lookup_imports li
    WHERE li.workspace_id = @workspace_id AND li.attachment_id = @id
) users_of_attachment
LIMIT 1;

-- name: AccessTrashBatch :one
SELECT root_type, root_id FROM trash_batches WHERE id = @id AND workspace_id = @workspace_id;

-- name: AccessTrashRoots :many
-- Every batch with its root; an item root also names its list (the box is the list's).
SELECT b.id, b.root_type, b.root_id, i.list_id AS item_list_id
FROM trash_batches b
LEFT JOIN items i ON b.root_type = 'item' AND i.id = b.root_id AND i.workspace_id = b.workspace_id
WHERE b.workspace_id = @workspace_id;

-- name: ListUserTopOrder :many
SELECT entry_type, entry_id, sort_order FROM user_top_order
WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- name: UpsertUserTopOrder :exec
-- The caller's own position of one top-level entry; other users' rows are never touched.
INSERT INTO user_top_order (workspace_id, user_id, entry_type, entry_id, sort_order)
VALUES (@workspace_id, @user_id, @entry_type, @entry_id, @sort_order)
ON CONFLICT (user_id, entry_type, entry_id) DO UPDATE SET sort_order = EXCLUDED.sort_order;

-- name: DeleteUserTopOrderOf :exec
-- Every user's order rows of the given folders or lists (purged, or no longer top level). There
-- is no foreign key, so the purge calls this explicitly (D-118).
DELETE FROM user_top_order WHERE workspace_id = @workspace_id AND entry_id = ANY(@ids::uuid[]);

-- name: LockAccessBackfill :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: CountOwnerlessTopFolders :one
SELECT count(*)::int FROM folders
WHERE workspace_id = @workspace_id AND parent_folder_id IS NULL AND owner_user_id IS NULL;

-- name: CountOwnerlessTopLists :one
SELECT count(*)::int FROM lists
WHERE workspace_id = @workspace_id AND folder_id IS NULL AND owner_user_id IS NULL;

-- name: AssignOwnerlessTopFolders :execrows
UPDATE folders SET owner_user_id = @owner_user_id
WHERE workspace_id = @workspace_id AND parent_folder_id IS NULL AND owner_user_id IS NULL;

-- name: AssignOwnerlessTopLists :execrows
UPDATE lists SET owner_user_id = @owner_user_id
WHERE workspace_id = @workspace_id AND folder_id IS NULL AND owner_user_id IS NULL;
