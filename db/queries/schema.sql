-- Smoke query so sqlc has something to compile (it fails on an empty queries dir). Domain queries start with B-05.

-- name: CountWorkspaces :one
SELECT count(*) FROM workspaces;
