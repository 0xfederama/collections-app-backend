// Package workspace owns the single workspace of the MVP: its idempotent
// bootstrap at startup and its settings (DATA_MODEL §2/§3, API.md §5).
package workspace

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/seed"
)

// bootstrapLockKey is the fixed application constant for pg_advisory_xact_lock
// (DATA_MODEL §3). Any other bootstrap-like task must pick a different key.
const bootstrapLockKey int64 = 0x436F6C6C41707001 // "CollApp" + 1

// Bootstrap makes sure exactly one workspace and its settings row exist and
// returns the workspace ID. It is safe to call from several processes at once:
// an advisory lock serialises the check-then-insert, and an existing workspace
// is never modified (a later WORKSPACE_NAME does not rename it).
// More than one workspace is a broken MVP invariant and fails loudly.
// The built-in types are seeded on every start (idempotent, see package seed).
func Bootstrap(ctx context.Context, db database.TxBeginner, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := database.InTx(ctx, db, func(q *sqlc.Queries) error {
		if err := q.LockWorkspaceBootstrap(ctx, bootstrapLockKey); err != nil {
			return fmt.Errorf("lock bootstrap: %w", err)
		}
		ids, err := q.ListWorkspaceIDs(ctx)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		switch len(ids) {
		case 0:
			if id, err = q.InsertWorkspace(ctx, name); err != nil {
				return fmt.Errorf("insert workspace: %w", err)
			}
			if err := q.InsertWorkspaceSettings(ctx, id); err != nil {
				return fmt.Errorf("insert workspace settings: %w", err)
			}
		case 1:
			id = ids[0]
		default:
			return fmt.Errorf("found more than one workspace: the MVP supports exactly one")
		}
		if err := seed.BuiltinTypes(ctx, q, id); err != nil {
			return fmt.Errorf("seed built-in type definitions: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap workspace: %w", err)
	}
	return id, nil
}
