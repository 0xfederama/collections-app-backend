-- Kind definitions are workspace scoped. B2/B3 use these reads and locks to
-- replace the fixed in-process kind registry.

-- name: SeedBuiltinKindDefinitions :exec
SELECT seed_builtin_kind_definitions(@workspace_id);

-- name: ListKindDefinitions :many
SELECT * FROM kind_definitions
WHERE workspace_id = @workspace_id
ORDER BY is_locked DESC, name, key;

-- name: GetKindDefinitionByKey :one
SELECT * FROM kind_definitions
WHERE workspace_id = @workspace_id AND key = @key;

-- name: LockKindDefinition :one
SELECT * FROM kind_definitions
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: ListKindFields :many
SELECT * FROM kind_fields
WHERE workspace_id = @workspace_id AND kind_id = @kind_id
ORDER BY position, id
FOR SHARE;

-- name: GetKindFieldByKey :one
SELECT * FROM kind_fields
WHERE workspace_id = @workspace_id AND kind_id = @kind_id AND key = @key;

-- name: LockKindField :one
SELECT * FROM kind_fields
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: InsertKindDefinition :one
INSERT INTO kind_definitions (workspace_id, key, name, default_icon, allows_price, default_sort_field, default_pinned_fields, updated_at)
VALUES (@workspace_id, @key, @name, sqlc.narg(default_icon), @allows_price, sqlc.narg(default_sort_field), sqlc.narg(default_pinned_fields), @now)
RETURNING *;

-- name: UpdateKindDefinitionName :one
UPDATE kind_definitions SET name = @name, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id RETURNING *;

-- name: DeleteKindDefinition :exec
DELETE FROM kind_definitions WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListActiveListsByKind :many
SELECT id, name FROM lists WHERE workspace_id = @workspace_id AND kind = @kind AND deleted_at IS NULL ORDER BY name, id;

-- name: InsertKindField :one
INSERT INTO kind_fields (workspace_id, kind_id, key, label, field_type, position, updated_at)
VALUES (@workspace_id, @kind_id, @key, @label, @field_type, @position, @now) RETURNING *;

-- name: UpdateKindFieldLabel :one
UPDATE kind_fields SET label = @label, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id RETURNING *;

-- name: DeleteKindField :exec
DELETE FROM kind_fields WHERE id = @id AND workspace_id = @workspace_id;

-- name: NextKindFieldPosition :one
SELECT COALESCE(MAX(position) + 1, 0)::int FROM kind_fields WHERE workspace_id = @workspace_id AND kind_id = @kind_id;

-- name: PrepareReorderKindFields :exec
UPDATE kind_fields f
SET position = position + (
      SELECT COALESCE(MAX(position), 0) + 1
      FROM kind_fields
      WHERE kind_fields.workspace_id = @workspace_id AND kind_fields.kind_id = @kind_id
    ),
    updated_at = @now
WHERE f.workspace_id = @workspace_id AND f.kind_id = @kind_id AND f.key = ANY(@keys::text[]);

-- name: ReorderKindFields :exec
UPDATE kind_fields f
SET position = (
      SELECT count(*)::int
      FROM kind_fields locked
      WHERE locked.workspace_id = @workspace_id AND locked.kind_id = @kind_id AND locked.is_locked
    ) + (o.ord - 1)::int,
    version = version + 1,
    updated_at = @now
FROM unnest(@keys::text[]) WITH ORDINALITY AS o(key, ord)
WHERE f.workspace_id = @workspace_id AND f.kind_id = @kind_id AND f.key = o.key;

-- name: StripKindFieldValues :exec
UPDATE items i SET metadata = i.metadata - @key::text, version = version + 1, updated_at = @now
FROM lists l WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id
  AND i.workspace_id = @workspace_id AND l.kind = @kind AND i.metadata ? @key::text;

-- name: CountKindFieldValues :one
SELECT count(*)::int FROM items i
JOIN lists l ON l.id = i.list_id AND l.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id AND l.kind = @kind AND i.metadata ? @key::text;

-- name: ResetKindFieldDefaults :exec
UPDATE kind_definitions
SET default_sort_field = CASE WHEN default_sort_field = @key THEN NULL ELSE default_sort_field END,
    default_pinned_fields = CASE
      WHEN default_pinned_fields ? @key::text THEN default_pinned_fields - @key::text
      ELSE default_pinned_fields
    END,
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ResetListKindFieldPins :exec
UPDATE lists
SET pinned_fields = NULL, version = version + 1, updated_at = @now
WHERE workspace_id = @workspace_id AND kind = @kind AND pinned_fields ? @key::text;

-- name: ResetListKindFieldSort :exec
UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = @now
WHERE workspace_id = @workspace_id AND kind = @kind AND sort_mode = @key;

-- name: TouchKindDefinition :exec
UPDATE kind_definitions SET version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;
