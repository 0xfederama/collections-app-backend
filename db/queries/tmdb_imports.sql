-- F013-B3 TMDB import provenance. Every query is scoped to one workspace.

-- name: GetLookupListType :one
SELECT type FROM lists
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: LockLookupItem :one
SELECT i.*, l.type
FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.id = @id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL
FOR UPDATE OF i;

-- name: InsertLookupItem :one
INSERT INTO items (workspace_id, list_id, title, website_url, metadata, image_attachment_id,
                   sort_order, created_at, updated_at)
VALUES (@workspace_id, @list_id, @title, sqlc.narg(website_url), @metadata,
        sqlc.narg(image_attachment_id), @sort_order, @now, @now)
RETURNING id;

-- name: GetTMDBImport :one
SELECT t.*, i.version AS item_version
FROM tmdb_imports t
JOIN items i ON i.id = t.item_id AND i.workspace_id = t.workspace_id
WHERE t.item_id = @item_id AND t.workspace_id = @workspace_id;

-- name: UpsertTMDBImport :exec
INSERT INTO tmdb_imports (
  workspace_id, item_id, tmdb_id, type, imported_fields, imported_title, original_title,
  attachment_id, poster_path, poster_fetched_at, poster_refresh_after, poster_expires_at, fetched_at, refresh_after, expires_at
) VALUES (
  @workspace_id, @item_id, @tmdb_id, @type, @imported_fields, sqlc.narg(imported_title), @original_title,
  sqlc.narg(attachment_id), sqlc.narg(poster_path), sqlc.narg(poster_fetched_at), sqlc.narg(poster_refresh_after), sqlc.narg(poster_expires_at),
  @fetched_at, @refresh_after, @expires_at
)
ON CONFLICT (item_id) DO UPDATE SET
  tmdb_id = EXCLUDED.tmdb_id,
  type = EXCLUDED.type,
  imported_fields = EXCLUDED.imported_fields,
  imported_title = EXCLUDED.imported_title,
  original_title = EXCLUDED.original_title,
  attachment_id = EXCLUDED.attachment_id,
  poster_path = EXCLUDED.poster_path,
  poster_fetched_at = EXCLUDED.poster_fetched_at,
  poster_refresh_after = EXCLUDED.poster_refresh_after,
  poster_expires_at = EXCLUDED.poster_expires_at,
  fetched_at = EXCLUDED.fetched_at,
  refresh_after = EXCLUDED.refresh_after,
  expires_at = EXCLUDED.expires_at
WHERE tmdb_imports.workspace_id = EXCLUDED.workspace_id;

-- name: ListDueTMDBImports :many
SELECT t.*, i.version AS item_version, i.metadata, i.deleted_at, l.type AS current_type
FROM tmdb_imports t
JOIN items i ON i.id = t.item_id AND i.workspace_id = t.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE t.workspace_id = @workspace_id
  AND (t.refresh_after <= @now OR (t.poster_refresh_after IS NOT NULL AND t.poster_refresh_after <= @now))
ORDER BY t.expires_at, t.item_id
LIMIT @batch_limit;

-- name: ListExpiredTMDBImportIDs :many
SELECT item_id FROM tmdb_imports
WHERE workspace_id = @workspace_id
  AND (expires_at <= @now OR (poster_expires_at IS NOT NULL AND poster_expires_at <= @now))
ORDER BY LEAST(expires_at, COALESCE(poster_expires_at, expires_at)), item_id
LIMIT @batch_limit;

-- name: LockTMDBImportItem :one
SELECT i.*, l.type, t.tmdb_id, t.imported_fields, t.imported_title, t.original_title,
       t.attachment_id AS imported_attachment_id
FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
JOIN tmdb_imports t ON t.item_id = i.id AND t.workspace_id = i.workspace_id
WHERE i.id = @id AND i.workspace_id = @workspace_id
FOR UPDATE OF i;

-- name: ClearTMDBPosterProvenance :exec
UPDATE tmdb_imports
SET attachment_id = NULL, poster_path = NULL, poster_fetched_at = NULL,
    poster_refresh_after = NULL, poster_expires_at = NULL
WHERE item_id = @item_id AND workspace_id = @workspace_id;

-- name: RetainTMDBPosterAfterMetadataExpiry :exec
-- Metadata is removed at its own deadline while the independently aged poster remains tracked.
UPDATE tmdb_imports
SET imported_fields = '{}'::jsonb, imported_title = NULL,
    fetched_at = poster_fetched_at, refresh_after = poster_refresh_after,
    expires_at = poster_expires_at
WHERE item_id = @item_id AND workspace_id = @workspace_id
  AND poster_fetched_at IS NOT NULL AND poster_refresh_after IS NOT NULL AND poster_expires_at IS NOT NULL;

-- name: UpdateLookupItem :exec
UPDATE items
SET title = @title,
    metadata = @metadata,
    image_url = sqlc.narg(image_url),
    image_attachment_id = sqlc.narg(image_attachment_id),
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: DeleteTMDBImport :execrows
DELETE FROM tmdb_imports
WHERE item_id = @item_id AND workspace_id = @workspace_id;
