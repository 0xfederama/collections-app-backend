# database

## Purpose

Owns the PostgreSQL layer: the pgx connection pool, the transaction helper, the SQL migrations (000001 creates the schema, 000002 to 000008 evolve it), and sqlc-generated query code. Schema intent lives in `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/database/database.go`: `NewPool`, `TxBeginner`, `InTx`
- `internal/database/intx_test.go`: commit, rollback, panic and `NewPool` tests
- `db/migrations/000001_init.{up,down}.sql`: pg_trgm and the 8 tables; down drops everything
- `db/migrations/000002_restructure.{up,down}.sql`: F-007 data move, lossy down
- `db/migrations/000003` to `000005`: kind fields (F-002), `pinned_fields` JSONB, `price` sort
- `db/migrations/000006_generic_no_price.{up,down}.sql`: generic prices move to a "From 1.2" content block; generic lists lose pinned `price` and price sort (F008-B1)
- `db/migrations/000007_price_kinds.{up,down}.sql`: movie/tv_show (and generic, a no-op by then) prices move to a "From 1.3" content block (F009-B1)
- `db/migrations/000008_pinned_fields_ordered.{up,down}.sql`: `pinned_fields` becomes an ordered array of at most 2 keys (F009-B2)
- `internal/database/migration_f007_test.go`, `migration_f002b2_test.go`, `migration_f008b1_test.go`, `migration_f009_test.go`: fixture tests for 000002, 000003, 000006, 000007 and 000008
- `db/queries/schema.sql`: smoke query, sqlc input
- `db/queries/workspace.sql`: workspace bootstrap and settings queries (see `workspace` module)
- `internal/testdb/testdb.go`: test-only shared testcontainers Postgres, `NewMigrated`, `Terminate`
- `sqlc.yaml`: sqlc config (pgx/v5, uuid to google/uuid, timestamptz to time.Time)
- `internal/database/sqlc/`: generated code, never edit
- `internal/database/migrations_test.go`, `schema_test.go`: up/down/up cycle, constraint names, SQLSTATE, D-051

## Interfaces

- `NewPool(ctx, databaseURL string, maxConns int32) (*pgxpool.Pool, error)`: parses URL, sets `MaxConns`, pings; closes the pool on ping failure
- `TxBeginner`: `Begin(ctx) (pgx.Tx, error)`; satisfied by `*pgxpool.Pool`
- `InTx(ctx, db TxBeginner, fn func(*sqlc.Queries) error) error`: commits if `fn` returns nil, rolls back on error or panic
- `make migrate`: applies migrations; `make generate`: runs `go tool sqlc generate`

## Gotchas

- `InTx` re-panics after rolling back. Rollback uses `context.WithoutCancel`, so a cancelled ctx still rolls back. A failed rollback is joined into the returned error.
- Multi-statement writes must go through `InTx` (ARCHITECTURE §2).
- The 000001 down drops all tables and the pg_trgm extension, so it erases all data.
- 000002 down is lossy: summary and status come back NULL, split lists stay split, and the default currency returns as EUR. Take a backup first.
- 000003 down is a no-op (make and model stay in content). 000004 down drops every `pinned_fields`; 000005 down turns `price` sorts into `manual`; 000006 down is a no-op (moved prices stay in content). 000007 down is a no-op (moved prices stay in content); 000008 down puts every key back in `under` (order kept, `right` empty).
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir (hence `schema.sql`) and on a recursive CTE filtering with a plain `@param` (use `sqlc.arg(x)::uuid`, see `folders.sql`).
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- Migration tests use the `pgx5://` scheme and one testcontainers database per test; `internal/testdb` shares one container per test binary.
