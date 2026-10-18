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
ORDER BY position, id;

-- name: GetKindFieldByKey :one
SELECT * FROM kind_fields
WHERE workspace_id = @workspace_id AND kind_id = @kind_id AND key = @key;

-- name: LockKindField :one
SELECT * FROM kind_fields
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;
