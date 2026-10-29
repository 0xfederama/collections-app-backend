-- Built-in type seeding (internal/seed). The data lives in Go; these are plain statements.
-- Every statement keeps existing rows untouched (ON CONFLICT DO NOTHING, pins only while NULL).

-- name: InsertBuiltinTypeDefinition :exec
-- Locked by definition: is_locked is always true for a built-in type.
INSERT INTO type_definitions (workspace_id, key, name, default_icon, allows_price, default_sort_field, is_locked)
VALUES (@workspace_id, @key, @name, sqlc.narg(default_icon), @allows_price, sqlc.narg(default_sort_field), true)
ON CONFLICT (workspace_id, key) DO NOTHING;

-- name: SetBuiltinDefaultPinnedFields :exec
-- Sets the default pins only where none are set yet, so a user edit is never overwritten.
UPDATE type_definitions
SET default_pinned_fields = sqlc.arg(pins)::jsonb
WHERE workspace_id = @workspace_id AND key = @key AND is_locked AND default_pinned_fields IS NULL;

-- name: InsertBuiltinTypeField :exec
-- The type row is looked up by key; if the type does not exist the insert adds nothing.
INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position, is_locked)
SELECT k.workspace_id, k.id, sqlc.arg(key)::text, sqlc.arg(label)::text, sqlc.arg(field_type)::text, sqlc.arg(position)::int, true
FROM type_definitions k
WHERE k.workspace_id = @workspace_id AND k.key = @type_key
ON CONFLICT (type_id, key) DO NOTHING;
