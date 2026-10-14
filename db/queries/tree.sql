-- Structural writes (create, move, reorder) of folders and lists take this
-- lock first, so they run one at a time per workspace. One lock for the whole
-- tree also makes the folder cycle check race-free (two concurrent moves
-- A→B and B→A would otherwise both pass it). Released at commit or rollback.

-- name: LockTree :exec
SELECT pg_advisory_xact_lock(hashtextextended('tree:' || sqlc.arg(workspace_id)::uuid::text, 0));
