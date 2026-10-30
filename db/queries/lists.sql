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
INSERT INTO lists (workspace_id, folder_id, name, type, description, display_mode, sort_mode, pinned_fields, icon, icon_color, sort_order, created_at, updated_at)
VALUES (@workspace_id, sqlc.narg(folder_id), @name, sqlc.narg(type), sqlc.narg(description), @display_mode, @sort_mode, sqlc.narg(pinned_fields), sqlc.narg(icon), sqlc.narg(icon_color), @sort_order, @now, @now)
RETURNING *;

-- name: UpdateList :one
-- A full replacement of the editable fields (description is Markdown, up to 64 KiB, checked in the service; type has UpdateListType): the service merges the PATCH into the locked row first.
UPDATE lists
SET name = @name, description = sqlc.narg(description), display_mode = @display_mode, sort_mode = @sort_mode,
    pinned_fields = sqlc.narg(pinned_fields), icon = sqlc.narg(icon), icon_color = sqlc.narg(icon_color),
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: UpdateListType :one
-- The service clears the items' type fields (ClearListItemsMetadata) in the same transaction. Pinned fields reset to the type default.
UPDATE lists
SET type = sqlc.narg(type), pinned_fields = NULL, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: MoveList :one
UPDATE lists
SET folder_id = sqlc.narg(folder_id), sort_order = @sort_order, version = version + 1, updated_at = @now
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

-- name: FolderPath :many
-- Ancestor folders of a folder, root first, including the folder itself.
WITH RECURSIVE chain(id, parent_folder_id, name, depth) AS (
    SELECT f.id, f.parent_folder_id, f.name, 1 AS depth FROM folders f
    WHERE f.id = @folder_id AND f.workspace_id = @workspace_id AND f.deleted_at IS NULL
  UNION ALL
    SELECT p.id, p.parent_folder_id, p.name, c.depth + 1 FROM folders p
    JOIN chain c ON p.id = c.parent_folder_id
    WHERE p.workspace_id = @workspace_id AND p.deleted_at IS NULL AND c.depth < 10000
)
SELECT id, name FROM chain ORDER BY depth DESC;
