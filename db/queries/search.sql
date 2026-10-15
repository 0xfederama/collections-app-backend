-- Search (DATA_MODEL §6). Items of trashed lists and folders are trashed too (batch cascade, D-004),
-- so deleted_at IS NULL on the item is the whole visibility rule. The columns match ListItems, so
-- internal/items maps both with the same function.

-- name: SearchItems :many
-- @like_q is the escaped LIKE fragment (\, %, _ escaped in Go); @q is the raw text, used for the exact-match rank.
SELECT i.*,
       l.kind              AS kind,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename,
       COALESCE((SELECT max(v.version) FROM item_content_versions v WHERE v.item_id = i.id), 0)::int AS content_version
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id AND i.deleted_at IS NULL
  AND lower(i.title) LIKE '%' || lower(@like_q::text) || '%' ESCAPE '\'
ORDER BY (lower(i.title) = lower(@q::text)) DESC, lower(i.title), i.id
LIMIT @page_limit OFFSET @page_offset;

-- name: CountSearchItems :one
SELECT count(*)::int AS total FROM items i
WHERE i.workspace_id = @workspace_id AND i.deleted_at IS NULL
  AND lower(i.title) LIKE '%' || lower(@like_q::text) || '%' ESCAPE '\';

-- name: ListSearchFolders :many
-- Active folders, just enough to walk a path.
SELECT id, name, parent_folder_id FROM folders
WHERE workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ListSearchLists :many
-- Active lists, just enough to name the last path segment and find its folder.
SELECT id, name, folder_id FROM lists
WHERE workspace_id = @workspace_id AND deleted_at IS NULL;

-- name: ListRecentItems :many
-- Recently updated active items (Home, B-12). The columns match ListItems, like SearchItems. No ancestor
-- check: an item in a trashed list or folder is trashed itself (batch cascade). Not paginated, so no
-- index: at MVP scale the sort over active items is a few ms (EXPLAIN in the PR).
SELECT i.*,
       l.kind              AS kind,
       a.width             AS image_width,
       a.height            AS image_height,
       a.original_filename AS image_original_filename,
       COALESCE((SELECT max(v.version) FROM item_content_versions v WHERE v.item_id = i.id), 0)::int AS content_version
FROM items i
LEFT JOIN attachments a ON a.id = i.image_attachment_id AND a.workspace_id = i.workspace_id
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id AND i.deleted_at IS NULL
ORDER BY i.updated_at DESC, i.id
LIMIT @page_limit;
