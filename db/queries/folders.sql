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

-- name: FolderIsSelfOrAncestor :one
-- Counts 1 when folder_id is the destination itself or one of its ancestors: the
-- walk goes up from the destination. The depth cap keeps it finite even on corrupt data.
WITH RECURSIVE chain(id, parent_folder_id, depth) AS (
    SELECT f.id, f.parent_folder_id, 1 AS depth FROM folders f
    WHERE f.id = @destination_id AND f.workspace_id = @workspace_id AND f.deleted_at IS NULL
  UNION ALL
    SELECT p.id, p.parent_folder_id, c.depth + 1 FROM folders p
    JOIN chain c ON p.id = c.parent_folder_id
    WHERE p.workspace_id = @workspace_id AND p.deleted_at IS NULL AND c.depth < 10000
)
SELECT count(*)::int AS matches FROM chain WHERE chain.id = sqlc.arg(folder_id)::uuid;

-- name: ListActiveFolderIDsByParent :many
SELECT id FROM folders
WHERE workspace_id = @workspace_id AND deleted_at IS NULL
  AND parent_folder_id IS NOT DISTINCT FROM sqlc.narg(parent_folder_id)::uuid
ORDER BY sort_order, id;

-- name: ReorderFolders :exec
-- Rewrites sort_order to the position in ids (0..n-1). Versions stay (DATA_MODEL §7).
UPDATE folders f
SET sort_order = (o.ord - 1)::int, updated_at = @now
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS o(id, ord)
WHERE f.id = o.id AND f.workspace_id = @workspace_id AND f.deleted_at IS NULL;
