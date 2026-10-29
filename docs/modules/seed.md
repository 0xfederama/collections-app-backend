# seed

## Purpose

Writes the five built-in types (movie, tv_show, book, vehicle, wishlist) and their 27 locked fields and default pins into a workspace. The data lives in Go, not SQL, and runs on every bootstrap. Type rules: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md` §2 and F-018.

## Key files

- `internal/seed/seed.go`: `builtins` table and `BuiltinTypes`
- `internal/seed/seed_test.go`: idempotency, existing-row and pin tests
- `db/queries/seed.sql`: insert and pin statements (`ON CONFLICT DO NOTHING`, pins only while NULL)
- `internal/workspace/bootstrap.go`: the only caller

## Interfaces

- `BuiltinTypes(ctx, q *sqlc.Queries, workspaceID uuid.UUID) error`: runs inside the caller's transaction; returns the first error with the type or field key

## Gotchas

- Idempotent: existing type rows, fields and default pins are never overwritten, so a user's edit of a locked label or of the pins survives a restart.
- Pins are set only while `default_pinned_fields IS NULL`; a row that exists with pins keeps them.
- A new built-in field added to Go reaches existing workspaces on the next start only if its type row exists; no migration is needed.
- Not a trigger: a raw SQL insert into a fresh workspace gets no built-ins until `Bootstrap` runs (tests insert rows directly).
- Built-in types are locked by definition (`is_locked = true`).
