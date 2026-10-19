-- Workspace bootstrap and settings (DATA_MODEL §2/§3, API.md §5). Every query filters by workspace_id.

-- name: LockWorkspaceBootstrap :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: ListWorkspaceIDs :many
-- Two rows are enough to tell "exactly one" from "more than one".
SELECT id FROM workspaces ORDER BY created_at, id LIMIT 2;

-- name: InsertWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING id;

-- name: InsertWorkspaceSettings :exec
INSERT INTO workspace_settings (workspace_id) VALUES ($1);

-- name: GetSettings :one
SELECT w.name AS workspace_name, s.streaming_country_code, s.metadata_language, s.markdown_history_limit, s.trash_retention_days, s.preferred_store, s.version
FROM workspace_settings s
JOIN workspaces w ON w.id = s.workspace_id
WHERE s.workspace_id = $1;

-- name: LockSettings :one
-- Row lock that serialises concurrent PATCHes of the same workspace.
SELECT w.name AS workspace_name, s.streaming_country_code, s.metadata_language, s.markdown_history_limit, s.trash_retention_days, s.preferred_store, s.version
FROM workspace_settings s
JOIN workspaces w ON w.id = s.workspace_id
WHERE s.workspace_id = $1
FOR UPDATE OF s;

-- name: UpdateSettings :exec
UPDATE workspace_settings
SET streaming_country_code = $2,
    metadata_language      = $3,
    markdown_history_limit = $4,
    trash_retention_days   = $5,
    preferred_store        = $6,
    version                = version + 1,
    updated_at             = now()
WHERE workspace_id = $1;

-- name: UpdateWorkspaceName :exec
UPDATE workspaces
SET name = $2, version = version + 1, updated_at = now()
WHERE id = $1;

-- name: ClearItemStreamingLinks :exec
-- The JustWatch page is per country: when the streaming country changes, every imported
-- streaming.link goes until the next lookup refresh. Derived data, so no version bump.
UPDATE items
SET metadata = metadata #- '{streaming,link}'
WHERE workspace_id = $1 AND metadata #>> '{streaming,link}' IS NOT NULL;

-- name: ClearStreamingLinkSnapshots :exec
UPDATE lookup_imports
SET imported_fields = imported_fields - 'streaming.link'
WHERE workspace_id = $1 AND imported_fields ->> 'streaming.link' IS NOT NULL;
