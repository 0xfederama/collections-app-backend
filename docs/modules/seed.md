# seed

## Purpose

Writes the five built-in types (movie, tv_show, book, vehicle, wishlist) and their 27 locked fields, plus each type's default sort field, into a workspace. The data lives in Go, not SQL, and runs on every bootstrap. Type rules: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md` §2 and F-018.

## Key files

- `internal/seed/seed.go`: `builtins` table and `BuiltinTypes`
- `internal/seed/seed_test.go`: idempotency and existing-row tests
- `db/queries/seed.sql`: type and field insert statements (`ON CONFLICT DO NOTHING`)
- `internal/workspace/bootstrap.go`: the only caller

## Interfaces

- `BuiltinTypes(ctx, q *sqlc.Queries, workspaceID uuid.UUID) error`: runs inside the caller's transaction; returns the first error with the type or field key

## Gotchas

- Idempotent: existing type rows and fields are never overwritten, so a user's edit of a locked label survives a restart.
- Default pins no longer exist (F-022); the web owns the fixed pins.
- A new built-in field added to Go reaches existing workspaces on the next start only if its type row exists; no migration is needed.
- Not a trigger: a raw SQL insert into a fresh workspace gets no built-ins until `Bootstrap` runs (tests insert rows directly).
- Built-in types are locked by definition (`is_locked = true`).
