-- Item read model: GetItem and ListItems return the same columns, so one mapper
-- (internal/items) serves both. kind is not stored on the item: it comes from its list (F-007). The image columns come from the attachment the
-- item points to (NULL for no image or an external URL). content_version is the
-- highest saved Markdown version, 0 when there is none (D-052).

-- name: GetItem :one
SELECT i.*,
       l.kind              AS kind,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename,
       COALESCE((SELECT max(v.version) FROM item_content_versions v WHERE v.item_id = i.id), 0)::int AS content_version
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.id = @id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL;

-- name: LockItem :one
SELECT * FROM items
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR UPDATE;

-- name: ListItems :many
-- @sort is manual, title or updated (kind_key has its own query, ListItemsByKindKey); @q is an already escaped LIKE fragment ('' = no filter).
SELECT i.*,
       l.kind              AS kind,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename,
       COALESCE((SELECT max(v.version) FROM item_content_versions v WHERE v.item_id = i.id), 0)::int AS content_version
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.list_id = @list_id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL
  AND lower(i.title) LIKE '%' || lower(@q::text) || '%' ESCAPE '\'
ORDER BY
  CASE WHEN @sort::text = 'manual' THEN i.sort_order END ASC,
  CASE WHEN @sort::text = 'title' THEN lower(i.title) END ASC,
  CASE WHEN @sort::text = 'updated' THEN i.updated_at END DESC,
  i.id
LIMIT @page_limit OFFSET @page_offset;

-- name: ListItemsByKindKey :many
-- Sort by the kind's sort key, ascending, empty (missing or non-numeric) values last. @key is the metadata key.
SELECT i.*,
       l.kind              AS kind,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename,
       COALESCE((SELECT max(v.version) FROM item_content_versions v WHERE v.item_id = i.id), 0)::int AS content_version
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.list_id = @list_id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL
  AND lower(i.title) LIKE '%' || lower(@q::text) || '%' ESCAPE '\'
ORDER BY
  CASE WHEN jsonb_typeof(i.metadata -> sqlc.arg(key)::text) = 'number'
       THEN (i.metadata ->> sqlc.arg(key)::text)::numeric END ASC NULLS LAST,
  i.id
LIMIT @page_limit OFFSET @page_offset;

-- name: CountItems :one
SELECT count(*)::int AS total FROM items i
WHERE i.list_id = @list_id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL
  AND lower(i.title) LIKE '%' || lower(@q::text) || '%' ESCAPE '\';

-- name: GetActiveListForItems :one
SELECT id, name, folder_id, sort_mode, kind FROM lists
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: NextItemSortOrder :one
SELECT (COALESCE(MAX(sort_order) + 1, 0))::int AS next_order
FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: InsertItem :one
INSERT INTO items (workspace_id, list_id, title, price_min, price_max, currency,
                   website_url, metadata, sort_order, created_at, updated_at)
VALUES (@workspace_id, @list_id, @title, sqlc.narg(price_min),
        sqlc.narg(price_max), sqlc.narg(currency), sqlc.narg(website_url), @metadata, @sort_order, @now, @now)
RETURNING id;

-- name: UpdateItem :one
-- A full replacement of the editable fields: the service merges the PATCH into the locked row first.
-- Image columns are not touched here (B-09 owns them).
UPDATE items
SET title = @title,
    price_min = sqlc.narg(price_min), price_max = sqlc.narg(price_max), currency = sqlc.narg(currency),
    website_url = sqlc.narg(website_url), metadata = @metadata,
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING id;

-- name: MoveItem :one
UPDATE items
SET list_id = @list_id, sort_order = @sort_order, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING id;

-- name: ListActiveItemIDsByList :many
SELECT id FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id AND deleted_at IS NULL
ORDER BY sort_order, id;

-- name: ReorderItems :exec
-- Rewrites sort_order to the position in ids (0..n-1). Versions stay (DATA_MODEL §7).
UPDATE items i
SET sort_order = (o.ord - 1)::int, updated_at = @now
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS o(id, ord)
WHERE i.id = o.id AND i.workspace_id = @workspace_id AND i.list_id = @list_id AND i.deleted_at IS NULL;

-- name: CountListItems :one
-- All items of a list, trashed ones included (a kind change clears all of them).
SELECT count(*)::int AS total FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id;

-- name: ListItemIDsOfList :many
SELECT id FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id
ORDER BY sort_order, id;

-- name: ClearListItemsMetadata :execrows
-- A kind change empties the kind fields of every item of the list (trashed ones too).
UPDATE items SET metadata = '{}', version = version + 1, updated_at = @now
WHERE list_id = @list_id AND workspace_id = @workspace_id AND metadata <> '{}';
