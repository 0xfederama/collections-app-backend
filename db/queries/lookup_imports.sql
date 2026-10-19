-- F013-B3 / F-016 lookup import provenance (provider tmdb or openlibrary). Every query is scoped to one workspace.
-- Retention (refresh_after, expires_at, poster_*) exists only for provider 'tmdb' (D-089); Open Library
-- rows have NULL deadlines (D-095), and every maintenance query below filters on provider = 'tmdb'.

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

-- name: ListDueTMDBImports :many
SELECT t.*, i.version AS item_version, i.metadata, i.deleted_at, l.type AS current_type
FROM lookup_imports t
JOIN items i ON i.id = t.item_id AND i.workspace_id = t.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE t.workspace_id = @workspace_id AND t.provider = 'tmdb'
  AND (t.refresh_after <= @now OR (t.poster_refresh_after IS NOT NULL AND t.poster_refresh_after <= @now))
ORDER BY t.expires_at, t.item_id
LIMIT @batch_limit;

-- name: ListExpiredTMDBImportIDs :many
SELECT item_id FROM lookup_imports
WHERE workspace_id = @workspace_id AND provider = 'tmdb'
  AND (expires_at <= @now OR (poster_expires_at IS NOT NULL AND poster_expires_at <= @now))
ORDER BY LEAST(expires_at, COALESCE(poster_expires_at, expires_at)), item_id
LIMIT @batch_limit;

-- name: LockTMDBImportItem :one
SELECT i.*, l.type, t.provider, t.source_id, t.imported_fields, t.imported_title, t.original_title,
       t.attachment_id AS imported_attachment_id
FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
JOIN lookup_imports t ON t.item_id = i.id AND t.workspace_id = i.workspace_id
WHERE i.id = @id AND i.workspace_id = @workspace_id
FOR UPDATE OF i;

-- name: ClearTMDBPosterProvenance :exec
UPDATE lookup_imports
SET attachment_id = NULL, poster_path = NULL, poster_fetched_at = NULL,
    poster_refresh_after = NULL, poster_expires_at = NULL
WHERE item_id = @item_id AND workspace_id = @workspace_id;

-- name: RetainTMDBPosterAfterMetadataExpiry :exec
-- Metadata is removed at its own deadline while the independently aged poster remains tracked.
UPDATE lookup_imports
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

-- name: DeleteLookupImport :execrows
DELETE FROM lookup_imports
WHERE item_id = @item_id AND workspace_id = @workspace_id;

-- name: ListStreamingLinkBackfillCandidates :many
-- One-time backfill of streaming.link (api 0.14.0): active TMDB movie/TV items whose item
-- country is @country, that list streaming platforms, have no link key, no "checked" marker in
-- the snapshots, and did not opt out of lookup. Keyset-paged by item id.
SELECT t.item_id, t.source_id, t.type
FROM lookup_imports t
JOIN items i ON i.id = t.item_id AND i.workspace_id = t.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE t.workspace_id = @workspace_id AND t.provider = 'tmdb'
  AND t.item_id > @after_id
  AND i.deleted_at IS NULL AND l.type = t.type AND t.type IN ('movie', 'tv_show')
  AND i.metadata ->> 'tmdb_id' = t.source_id
  AND COALESCE(i.metadata ->> 'lookup_disabled', 'false') <> 'true'
  AND jsonb_typeof(i.metadata -> 'streaming') = 'object'
  AND i.metadata #>> '{streaming,country}' = @country::text
  AND jsonb_typeof(i.metadata #> '{streaming,platforms}') = 'array'
  AND jsonb_array_length(i.metadata #> '{streaming,platforms}') > 0
  AND NOT jsonb_exists(i.metadata -> 'streaming', 'link')
  AND NOT jsonb_exists(t.imported_fields, 'streaming.link')
ORDER BY t.item_id
LIMIT @batch_limit;

-- name: SetItemStreamingLink :exec
-- Derived data like ClearItemStreamingLinks: no version or updated_at bump, so ETags and
-- the recent-items order stay as they are.
UPDATE items
SET metadata = jsonb_set(metadata, '{streaming,link}', to_jsonb(@link::text))
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: SetStreamingLinkSnapshot :exec
-- Writes the imported-fields entry streaming.link: the link itself, or a "none:<COUNTRY>"
-- marker meaning "checked, TMDB has no link". The snapshot path is absent from the item, so
-- every later import write drops the marker and the settings country change clears it.
UPDATE lookup_imports
SET imported_fields = imported_fields || jsonb_build_object('streaming.link', @snapshot::text)
WHERE item_id = @item_id AND workspace_id = @workspace_id;
