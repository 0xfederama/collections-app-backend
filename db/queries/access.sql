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

-- name: AccessMembers :many
-- Every membership of the workspace (a handful of rows). Rows of folders that are not top level
-- or no longer exist are returned too: the visibility rule ignores them.
SELECT folder_id, user_id FROM folder_members WHERE workspace_id = @workspace_id;

-- name: FolderVisibleTo :one
-- Visibility of a folder in any state, read inside a transaction (after the tree lock) to re-check
-- what the request-level snapshot saw: the box is the user's, or the user is a member of its
-- top-level folder. The depth cap keeps a corrupt cycle finite.
WITH RECURSIVE chain AS (
    SELECT f.id, f.parent_folder_id, f.owner_user_id, 0 AS depth
    FROM folders f WHERE f.id = sqlc.arg(id)::uuid AND f.workspace_id = sqlc.arg(workspace_id)::uuid
  UNION ALL
    SELECT p.id, p.parent_folder_id, p.owner_user_id, chain.depth + 1
    FROM folders p JOIN chain ON p.id = chain.parent_folder_id
    WHERE p.workspace_id = sqlc.arg(workspace_id)::uuid AND chain.depth < 1000
)
SELECT EXISTS (
    SELECT 1 FROM chain
    WHERE chain.parent_folder_id IS NULL
      AND (chain.owner_user_id = sqlc.arg(user_id)::uuid
           OR EXISTS (SELECT 1 FROM folder_members m WHERE m.folder_id = chain.id AND m.user_id = sqlc.arg(user_id)::uuid))
)::bool AS visible;

-- name: ListBoxRow :one
-- The folder and owner of a list in any state, to resolve its box inside a transaction.
SELECT folder_id, owner_user_id FROM lists WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListFolderMembers :many
SELECT folder_id, user_id FROM folder_members
WHERE workspace_id = @workspace_id AND folder_id = @folder_id
ORDER BY created_at, user_id;

-- name: ListAllFolderMembers :many
SELECT folder_id, user_id FROM folder_members
WHERE workspace_id = @workspace_id
ORDER BY created_at, user_id;

-- name: InsertFolderMember :exec
-- Idempotent: an existing member stays as it is.
INSERT INTO folder_members (workspace_id, folder_id, user_id, added_by_user_id)
VALUES (@workspace_id, @folder_id, @user_id, sqlc.narg(added_by_user_id))
ON CONFLICT (folder_id, user_id) DO NOTHING;

-- name: DeleteFolderMember :execrows
DELETE FROM folder_members WHERE workspace_id = @workspace_id AND folder_id = @folder_id AND user_id = @user_id;

-- name: DeleteUserTopOrderEntry :exec
-- One user's order row of one entry (a member leaves or is removed).
DELETE FROM user_top_order
WHERE workspace_id = @workspace_id AND user_id = @user_id AND entry_type = @entry_type AND entry_id = @entry_id;

-- name: DeleteFolderMembersOf :exec
-- Every member row of the given folders (purged). No foreign key, so the purge calls this (D-118).
DELETE FROM folder_members WHERE workspace_id = @workspace_id AND folder_id = ANY(@ids::uuid[]);

-- name: GetFolderAnyState :one
SELECT * FROM folders WHERE id = @id AND workspace_id = @workspace_id;
