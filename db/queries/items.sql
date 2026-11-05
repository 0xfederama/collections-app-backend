-- Item queries (internal/items/repository.go and the callers that lock or clear items). type is not
-- stored on the item: it comes from its list (F-007). The image columns come from the attachment the
-- item points to (NULL for no image or an external URL). The read queries do not read the content
-- versions: MaxItemContentVersions does that, in one query per page (D-052).

-- name: LockItem :one
SELECT * FROM items
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR UPDATE;

-- name: GetActiveListForItems :one
SELECT id, name, folder_id, sort_mode, type FROM lists
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: NextItemSortOrder :one
SELECT (COALESCE(MAX(sort_order) + 1, 0))::int AS next_order
FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ListActiveItemTitles :many
-- The titles of the active items of a list, for the duplicate_title rule of lookup creates (F-026).
SELECT title FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ClearListItemsPrice :execrows
-- A type change to a type without a price (or to no type) removes the price of every item of the list (trashed ones too; F008-B1).
UPDATE items SET price_min = NULL, price_max = NULL, currency = NULL, version = version + 1, updated_at = @now
WHERE list_id = @list_id AND workspace_id = @workspace_id AND (price_min IS NOT NULL OR price_max IS NOT NULL);

-- name: ClearListItemsMetadata :execrows
-- A type change empties the type fields of every item of the list (trashed ones too).
UPDATE items SET metadata = '{}', version = version + 1, updated_at = @now
WHERE list_id = @list_id AND workspace_id = @workspace_id AND metadata <> '{}';

-- name: ReadItem :one
-- One active item with its list type and attachment dimensions.
SELECT i.*,
       l.type              AS type,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.id = @id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL;

-- name: ReadListItems :many
-- The active items of one list (trashed lists included), in sort_order, id order.
SELECT i.*,
       l.type              AS type,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.list_id = @list_id AND i.workspace_id = @workspace_id AND i.deleted_at IS NULL
ORDER BY i.sort_order, i.id;

-- name: LockItemRow :one
-- The bare item row under FOR UPDATE. No joins: a joined row would lock the list and attachment too.
SELECT * FROM items
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
FOR UPDATE;

-- name: InsertItemRow :one
-- The editable columns only; the image columns are B-09's.
INSERT INTO items (workspace_id, list_id, title, price_min, price_max, currency,
                   website_url, metadata, sort_order, created_at, updated_at)
VALUES (@workspace_id, @list_id, @title, sqlc.narg(price_min),
        sqlc.narg(price_max), sqlc.narg(currency), sqlc.narg(website_url), @metadata, @sort_order, @now, @now)
RETURNING id;

-- name: UpdateItemRow :one
-- A full replacement of the editable columns; the caller merges the patch first. Image columns are not touched.
UPDATE items
SET title = @title,
    price_min = sqlc.narg(price_min), price_max = sqlc.narg(price_max), currency = sqlc.narg(currency),
    website_url = sqlc.narg(website_url), metadata = @metadata,
    version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING id;

-- name: MoveItemRow :one
UPDATE items
SET list_id = @list_id, sort_order = @sort_order, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL
RETURNING id;

-- name: SetItemRowSortOrder :exec
-- Sets one item's position. The version stays (DATA_MODEL §7).
UPDATE items
SET sort_order = @sort_order, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: NextItemSortOrderRow :one
SELECT (COALESCE(MAX(sort_order) + 1, 0))::int AS next_order
FROM items
WHERE list_id = @list_id AND workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: MaxItemContentVersions :many
-- The highest saved version of each listed item; an item with no versions has no row.
-- The workspace is checked through items, which content versions do not carry.
SELECT v.item_id, max(v.version)::int AS max_version
FROM item_content_versions v
JOIN items i ON i.id = v.item_id AND i.workspace_id = @workspace_id
WHERE v.item_id = ANY(@item_ids::uuid[])
GROUP BY v.item_id;

-- name: ReadItemListInfo :one
-- An active list's name, folder, sort mode, type and icon, for an item's path and sorts.
SELECT id, name, folder_id, sort_mode, type, icon, icon_color FROM lists
WHERE id = @id AND workspace_id = @workspace_id AND deleted_at IS NULL;
