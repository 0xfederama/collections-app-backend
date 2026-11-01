-- Type definitions are workspace scoped. Every query filters by workspace_id.

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

-- name: GetTypeFieldByKey :one
SELECT * FROM type_fields
WHERE workspace_id = @workspace_id AND type_id = @type_id AND key = @key;

-- name: LockTypeField :one
SELECT * FROM type_fields
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

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
    version = version + 1,
    updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ResetListTypeFieldSort :exec
UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = @now
WHERE workspace_id = @workspace_id AND type = sqlc.arg(type)::text AND sort_mode = @key;

-- name: TouchTypeDefinition :exec
UPDATE type_definitions SET version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ParkTypeFieldPosition :exec
-- Pass 1 of a reorder: moves one field to a position without a version bump.
-- The (type_id, position) unique constraint is not deferrable, so the caller
-- parks every field on a free position first, then sets the final ones.
UPDATE type_fields SET position = @position, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;

-- name: SetTypeFieldPosition :exec
-- Pass 2 of a reorder: the final position of one field, with its version bump.
UPDATE type_fields SET position = @position, version = version + 1, updated_at = @now
WHERE id = @id AND workspace_id = @workspace_id;
