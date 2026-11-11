-- item_count is the number of active items (DATA_MODEL §2).

-- name: GetList :one
SELECT l.*,
       (SELECT count(*) FROM items i
         WHERE i.list_id = l.id AND i.workspace_id = l.workspace_id AND i.deleted_at IS NULL)::int AS item_count
FROM lists l
WHERE l.id = @id AND l.workspace_id = @workspace_id AND l.deleted_at IS NULL;

-- name: LockList :one
SELECT * FROM lists
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR UPDATE;

-- name: ListActiveLists :many
SELECT l.*,
       (SELECT count(*) FROM items i
         WHERE i.list_id = l.id AND i.workspace_id = l.workspace_id AND i.deleted_at IS NULL)::int AS item_count
FROM lists l
WHERE l.workspace_id = @workspace_id AND l.deleted_at IS NULL
ORDER BY l.sort_order, l.id;

-- name: NextListSortOrder :one
SELECT (COALESCE(MAX(sort_order) + 1, 0))::int AS next_order
FROM lists
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
  AND folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)::uuid;

-- name: InsertList :one
-- Only a top-level list has an owner (the caller).
INSERT INTO lists (workspace_id, folder_id, owner_user_id, created_by_user_id, updated_by_user_id, name, type, description, display_mode, sort_mode, icon, icon_color, sort_order, created_at, updated_at)
VALUES (@workspace_id, sqlc.narg(folder_id), sqlc.narg(owner_user_id), sqlc.narg(caller_user_id), sqlc.narg(caller_user_id), @name, sqlc.narg(type), sqlc.narg(description), @display_mode, @sort_mode, sqlc.narg(icon), sqlc.narg(icon_color), @sort_order, @now, @now)
RETURNING *;

-- name: UpdateList :one
-- A full replacement of the editable fields (description is Markdown, up to 64 KiB, checked in the service; type has UpdateListType): the service merges the PATCH into the locked row first.
UPDATE lists
SET name = @name, description = sqlc.narg(description), display_mode = @display_mode, sort_mode = @sort_mode,
    icon = sqlc.narg(icon), icon_color = sqlc.narg(icon_color),
    version = version + 1, updated_at = @now, updated_by_user_id = COALESCE(sqlc.narg(caller_user_id)::uuid, updated_by_user_id)
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: UpdateListType :one
-- The service clears the items' type fields (ClearListItemsMetadata) in the same transaction.
UPDATE lists
SET type = sqlc.narg(type), version = version + 1, updated_at = @now, updated_by_user_id = COALESCE(sqlc.narg(caller_user_id)::uuid, updated_by_user_id)
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: MoveList :one
-- Owner rule as in MoveFolder.
UPDATE lists
SET folder_id = sqlc.narg(folder_id),
    owner_user_id = CASE WHEN sqlc.narg(folder_id)::uuid IS NULL THEN COALESCE(owner_user_id, sqlc.narg(caller_user_id)::uuid) END,
    sort_order = @sort_order, version = version + 1, updated_at = @now, updated_by_user_id = COALESCE(sqlc.narg(caller_user_id)::uuid, updated_by_user_id)
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: ListActiveListIDsByFolder :many
SELECT id FROM lists
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
  AND folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)::uuid
ORDER BY sort_order, id;

-- name: SetListSortOrder :exec
-- Sets one list's position. The version stays (DATA_MODEL §7).
UPDATE lists
SET sort_order = @sort_order, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

