-- Accounts (F-027). Every query filters by workspace_id.

-- name: GetUserByEmail :one
SELECT * FROM users WHERE workspace_id = $1 AND email = $2;

-- name: UpsertUserByEmail :one
-- Concurrent first requests of one person both end with the same row.
INSERT INTO users (workspace_id, email) VALUES ($1, $2)
ON CONFLICT (workspace_id, email) DO UPDATE SET email = EXCLUDED.email
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE workspace_id = $1 AND id = $2;

-- name: UpdateUserProfile :one
UPDATE users SET name = $3, avatar_url = $4, profile_fetched_at = $5, updated_at = $5
WHERE workspace_id = $1 AND id = $2
RETURNING *;
