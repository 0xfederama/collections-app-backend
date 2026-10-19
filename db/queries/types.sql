-- Type definitions are workspace scoped. B2/B3 use these reads and locks to
-- replace the fixed in-process type registry.

-- name: SeedBuiltinTypeDefinitions :exec
SELECT seed_builtin_type_definitions(@workspace_id);

-- name: ListTypeDefinitions :many
SELECT * FROM type_definitions
WHERE workspace_id = @workspace_id
ORDER BY is_locked DESC, name, key;

-- name: GetTypeDefinitionByKey :one
SELECT * FROM type_definitions
WHERE workspace_id = @workspace_id AND key = @key;

-- name: LockTypeDefinition :one
SELECT * FROM type_definitions
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: ListTypeFields :many
SELECT * FROM type_fields
WHERE workspace_id = @workspace_id AND type_id = @type_id
ORDER BY position, id
FOR SHARE;

-- name: ListStoresFieldKeys :many
-- One row per stores field of every type, to build store_links for a page of rows.
SELECT td.key AS type_key, tf.key AS field_key
FROM type_fields tf
JOIN type_definitions td ON td.id = tf.type_id AND td.workspace_id = tf.workspace_id
WHERE tf.workspace_id = @workspace_id AND tf.field_type = 'stores'
ORDER BY td.key, tf.position, tf.id;

-- name: GetTypeFieldByKey :one
SELECT * FROM type_fields
WHERE workspace_id = @workspace_id AND type_id = @type_id AND key = @key;

-- name: LockTypeField :one
SELECT * FROM type_fields
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: InsertTypeDefinition :one
INSERT INTO type_definitions (workspace_id, key, name, default_icon, allows_price, default_sort_field, default_pinned_fields, updated_at)
VALUES (@workspace_id, @key, @name, sqlc.narg(default_icon), @allows_price, sqlc.narg(default_sort_field), sqlc.narg(default_pinned_fields), @now)
RETURNING *;

-- name: UpdateTypeDefinition :one
UPDATE type_definitions
SET name = CASE WHEN @set_name::boolean THEN @name::text ELSE name END,
    default_icon = CASE WHEN @set_default_icon::boolean THEN sqlc.narg(default_icon) ELSE default_icon END,
    allows_price = CASE WHEN @set_allows_price::boolean THEN @allows_price::boolean ELSE allows_price END,
    default_sort_field = CASE WHEN @set_default_sort_field::boolean THEN sqlc.narg(default_sort_field) ELSE default_sort_field END,
    default_pinned_fields = CASE WHEN @set_default_pinned_fields::boolean THEN sqlc.narg(default_pinned_fields) ELSE default_pinned_fields END,
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id RETURNING *;

-- name: DeleteTypeDefinition :exec
DELETE FROM type_definitions WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListActiveListsByType :many
SELECT id, name FROM lists WHERE workspace_id = @workspace_id AND type = sqlc.arg(type)::text AND deleted_at IS NULL ORDER BY name, id;

-- name: InsertTypeField :one
INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position, updated_at)
VALUES (@workspace_id, @type_id, @key, @label, @field_type, @position, @now) RETURNING *;

-- name: UpdateTypeFieldLabel :one
UPDATE type_fields SET label = @label, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id RETURNING *;

-- name: DeleteTypeField :exec
DELETE FROM type_fields WHERE id = @id AND workspace_id = @workspace_id;

-- name: NextTypeFieldPosition :one
SELECT COALESCE(MAX(position) + 1, 0)::int FROM type_fields WHERE workspace_id = @workspace_id AND type_id = @type_id;

-- name: PrepareReorderTypeFields :exec
UPDATE type_fields f
SET position = position + (
      SELECT COALESCE(MAX(position), 0) + 1
      FROM type_fields
      WHERE type_fields.workspace_id = @workspace_id AND type_fields.type_id = @type_id
    ),
    updated_at = @now
WHERE f.workspace_id = @workspace_id AND f.type_id = @type_id AND f.key = ANY(@keys::text[]);

-- name: ReorderTypeFields :exec
UPDATE type_fields f
SET position = (
      SELECT count(*)::int
      FROM type_fields locked
      WHERE locked.workspace_id = @workspace_id AND locked.type_id = @type_id AND locked.is_locked
    ) + (o.ord - 1)::int,
    version = version + 1,
    updated_at = @now
FROM unnest(@keys::text[]) WITH ORDINALITY AS o(key, ord)
WHERE f.workspace_id = @workspace_id AND f.type_id = @type_id AND f.key = o.key;

-- name: StripTypeFieldValues :exec
UPDATE items i SET metadata = i.metadata - @key::text, version = i.version + 1, updated_at = @now
FROM lists l WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id
  AND i.workspace_id = @workspace_id AND l.type = sqlc.arg(type)::text AND i.metadata ? @key::text;

-- name: CountTypeFieldValues :one
SELECT count(*)::int FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id AND l.type = sqlc.arg(type)::text AND i.metadata ? @key::text;

-- name: ResetTypeFieldDefaults :exec
UPDATE type_definitions
SET default_sort_field = CASE WHEN default_sort_field = @key THEN NULL ELSE default_sort_field END,
    default_pinned_fields = CASE
      WHEN default_pinned_fields ? @key::text THEN default_pinned_fields - @key::text
      ELSE default_pinned_fields
    END,
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ResetListTypeFieldPins :exec
UPDATE lists
SET pinned_fields = NULL, version = version + 1, updated_at = @now
WHERE workspace_id = @workspace_id AND type = sqlc.arg(type)::text AND pinned_fields ? @key::text;

-- name: ResetListTypeFieldSort :exec
UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = @now
WHERE workspace_id = @workspace_id AND type = sqlc.arg(type)::text AND sort_mode = @key;

-- name: TouchTypeDefinition :exec
UPDATE type_definitions SET version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;
