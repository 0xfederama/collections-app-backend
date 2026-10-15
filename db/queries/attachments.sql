-- Image attachments (B-09). An item points to its attachment through
-- items.image_attachment_id; the attachment itself has no owner column (D-007).

-- name: InsertAttachment :exec
INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, original_filename, media_type,
                         size_bytes, width, height, sha256, created_at)
VALUES (@id, @workspace_id, @storage_key, @thumbnail_storage_key, sqlc.narg(original_filename), @media_type,
        @size_bytes, @width, @height, @sha256, @now);

-- name: GetAttachment :one
SELECT id, storage_key, thumbnail_storage_key, media_type, size_bytes
FROM attachments
WHERE id = @id AND workspace_id = @workspace_id;

-- name: SetItemImageAttachment :exec
UPDATE items
SET image_attachment_id = @attachment_id, image_url = NULL, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: SetItemImageURL :exec
UPDATE items
SET image_attachment_id = NULL, image_url = @image_url, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ClearItemImage :exec
UPDATE items
SET image_attachment_id = NULL, image_url = NULL, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: DeleteAttachment :one
-- Removes an attachment nothing points to anymore and returns its file keys.
-- Returns no row when it is still referenced (or already gone).
DELETE FROM attachments a
WHERE a.id = @id AND a.workspace_id = @workspace_id
  AND NOT EXISTS (SELECT 1 FROM items i WHERE i.image_attachment_id = a.id)
RETURNING a.storage_key, a.thumbnail_storage_key;

-- name: ListAttachmentKeys :many
-- Every file key the database knows, for the sweeper's orphan-file pass.
SELECT storage_key AS key FROM attachments WHERE workspace_id = sqlc.arg(workspace_id)::uuid
UNION ALL
SELECT thumbnail_storage_key FROM attachments WHERE workspace_id = sqlc.arg(workspace_id)::uuid;

-- name: ListUnreferencedAttachments :many
-- Attachments no item points to, created before @cutoff.
SELECT a.id FROM attachments a
WHERE a.workspace_id = @workspace_id AND a.created_at < @cutoff
  AND NOT EXISTS (SELECT 1 FROM items i WHERE i.image_attachment_id = a.id);

-- name: ListAttachmentsForStrip :many
-- Every original with the size and hash recorded for it, for the strip-images backfill.
SELECT id, storage_key, media_type, size_bytes, sha256
FROM attachments
WHERE workspace_id = @workspace_id
ORDER BY created_at, id;

-- name: UpdateAttachmentFile :execrows
-- Records the size and hash of an original rewritten without metadata.
UPDATE attachments
SET size_bytes = @size_bytes, sha256 = @sha256
WHERE id = @id AND workspace_id = @workspace_id;
