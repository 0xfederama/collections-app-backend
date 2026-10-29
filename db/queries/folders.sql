-- name: GetFolder :one
SELECT * FROM folders
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: LockFolder :one
SELECT * FROM folders
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR UPDATE;

-- name: ListActiveFolders :many
SELECT * FROM folders
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
ORDER BY sort_order, id;

-- name: NextFolderSortOrder :one
SELECT (COALESCE(MAX(sort_order) + 1, 0))::int AS next_order
FROM folders
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
  AND parent_folder_id IS NOT DISTINCT FROM sqlc.narg(parent_folder_id)::uuid;

-- name: InsertFolder :one
INSERT INTO folders (workspace_id, parent_folder_id, name, sort_order, created_at, updated_at)
VALUES (@workspace_id, sqlc.narg(parent_folder_id), @name, @sort_order, @now, @now)
RETURNING *;

-- name: RenameFolder :one
UPDATE folders
SET name = @name, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: MoveFolder :one
UPDATE folders
SET parent_folder_id = sqlc.narg(parent_folder_id), sort_order = @sort_order, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING *;

-- name: ListChildFolderIDs :many
SELECT id FROM folders
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
  AND parent_folder_id IS NOT DISTINCT FROM sqlc.narg(parent_folder_id)::uuid
ORDER BY sort_order, id;

-- name: SetFolderSortOrder :exec
-- Sets one folder's position. The version stays (DATA_MODEL §7).
UPDATE folders
SET sort_order = @sort_order, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;
