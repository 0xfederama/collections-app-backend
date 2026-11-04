-- F013-B3 / F-016 lookup import provenance (provider tmdb or openlibrary). Every query is scoped to one workspace.
-- Deadlines (refresh_after, expires_at, poster_*) are still written for provider 'tmdb' because
-- lookup_imports_retention_check requires them. Nothing reads them since D-123 (weekly refresh, no expiry);
-- the contract migration that drops them is a later release. Open Library rows have NULL deadlines (D-095).

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
INSERT INTO items (workspace_id, list_id, title, price_min, price_max, currency, website_url, metadata,
                   image_attachment_id, sort_order, created_at, updated_at)
VALUES (@workspace_id, @list_id, @title, sqlc.narg(price_min), sqlc.narg(price_max), sqlc.narg(currency),
        sqlc.narg(website_url), @metadata, sqlc.narg(image_attachment_id), @sort_order, @now, @now)
RETURNING id;

-- name: GetLookupImport :one
SELECT t.*, i.version AS item_version
FROM lookup_imports t
JOIN items i ON i.id = t.item_id AND i.workspace_id = t.workspace_id
WHERE t.item_id = @item_id AND t.workspace_id = @workspace_id;

-- name: UpsertLookupImport :exec
INSERT INTO lookup_imports (
  workspace_id, item_id, provider, source_id, type, imported_fields, imported_title, original_title,
  attachment_id, poster_path, poster_fetched_at, poster_refresh_after, poster_expires_at, fetched_at, refresh_after, expires_at
) VALUES (
  @workspace_id, @item_id, @provider, @source_id, @type, @imported_fields, sqlc.narg(imported_title), @original_title,
  sqlc.narg(attachment_id), sqlc.narg(poster_path), sqlc.narg(poster_fetched_at), sqlc.narg(poster_refresh_after), sqlc.narg(poster_expires_at),
  @fetched_at, sqlc.narg(refresh_after), sqlc.narg(expires_at)
)
ON CONFLICT (item_id) DO UPDATE SET
  provider = EXCLUDED.provider,
  source_id = EXCLUDED.source_id,
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
WHERE lookup_imports.workspace_id = EXCLUDED.workspace_id;

-- name: ListScheduledTMDBItems :many
-- The weekly refresh (D-123): movie and TV items with a numeric tmdb_id that are not trashed and not
-- lookup_disabled, and whose provenance (if any) was fetched before @fetched_before. An item with a typed
-- tmdb_id and no provenance yet is due at once and sorts first.
SELECT i.id AS item_id, i.version AS item_version
FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
LEFT JOIN lookup_imports t ON t.item_id = i.id AND t.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id
  AND i.deleted_at IS NULL
  AND l.deleted_at IS NULL
  AND l.type IN ('movie', 'tv_show')
  AND jsonb_typeof(i.metadata -> 'tmdb_id') = 'number'
  AND i.metadata -> 'lookup_disabled' IS DISTINCT FROM 'true'::jsonb
  AND (t.item_id IS NULL OR (t.provider = 'tmdb' AND t.fetched_at <= @fetched_before))
ORDER BY t.fetched_at NULLS FIRST, i.id
LIMIT @batch_limit;

-- name: UpdateLookupItem :exec
UPDATE items
SET title = @title,
    metadata = @metadata,
    image_url = sqlc.narg(image_url),
    image_attachment_id = sqlc.narg(image_attachment_id),
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListLookupItems :many
-- The list refresh (refreshListLookups): every active item of one list with its version and metadata, the
-- ones whose provenance was fetched longest ago first (never fetched first), then list order.
SELECT i.id AS item_id, i.version AS item_version, i.metadata
FROM items i
LEFT JOIN lookup_imports t ON t.item_id = i.id AND t.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id
  AND i.list_id = @list_id
  AND i.deleted_at IS NULL
ORDER BY t.fetched_at NULLS FIRST, i.sort_order, i.id;
