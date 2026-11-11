-- Markdown content versions (DATA_MODEL §2 item_content_versions, D-052). The current
-- version is the highest row of an item; no rows means version 0. Saves lock the item
-- row first, so the queries below that read "latest" run under that lock.

-- name: LockItemForContent :one
SELECT id FROM items
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: ItemIsActive :one
SELECT id FROM items
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: GetLatestContentVersion :one
SELECT item_id, version, body, size_bytes, sha256, created_at, author_user_id FROM item_content_versions
WHERE item_id = @item_id
ORDER BY version DESC
LIMIT 1;

-- name: GetContentVersion :one
SELECT item_id, version, body, size_bytes, sha256, created_at, author_user_id FROM item_content_versions
WHERE item_id = @item_id AND version = @version;

-- name: ListContentVersions :many
SELECT version, size_bytes, created_at, author_user_id FROM item_content_versions
WHERE item_id = @item_id
ORDER BY version DESC;

-- name: InsertContentVersion :exec
INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at, author_user_id)
VALUES (@item_id, @version, @body, @size_bytes, @sha256, @created_at, sqlc.narg(author_user_id));

-- name: PruneContentVersions :exec
-- Deletes every version up to and including @up_to; the latest version is never among them.
DELETE FROM item_content_versions
WHERE item_id = @item_id AND version <= @up_to;

-- name: TouchItemUpdatedAt :exec
-- A content save bumps updated_at (recently-updated sort) but not items.version.
UPDATE items SET updated_at = @now, updated_by_user_id = COALESCE(sqlc.narg(caller_user_id)::uuid, updated_by_user_id)
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;
