# database

## Purpose

Owns the PostgreSQL layer: the pgx connection pool, the transaction helper, the SQL migrations (000001 creates the schema, 000002 to 000005 evolve it), and sqlc-generated query code. Schema intent lives in `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/database/database.go`: `NewPool`, `TxBeginner`, `InTx`
- `internal/database/intx_test.go`: commit, rollback, panic and `NewPool` tests
- `db/migrations/000001_init.up.sql`: pg_trgm extension and the 8 tables
- `db/migrations/000001_init.down.sql`: drops everything (destructive)
- `db/migrations/000002_restructure.{up,down}.sql`: F-007 data move and lossy restore (see gotchas)
- `db/migrations/000003_kind_fields.{up,down}.sql`: drops motorcycle/camera make and model into content (F-002)
- `db/migrations/000004_list_pinned_fields.{up,down}.sql`: `lists.pinned_fields` JSONB (F-002)
- `db/migrations/000005_sort_price.{up,down}.sql`: `lists.sort_mode` accepts `price` (F-002)
- `db/migrations/000006_generic_no_price.{up,down}.sql`: generic items' price moves into a "From 1.2" content block and is cleared; generic lists lose pinned `price` and the price sort (F008-B1, down is a no-op)
- `internal/database/migration_f007_test.go`: `TestF007_MigrationRestructure` fixture test for 000002
- `internal/database/migration_f002b2_test.go`: `TestF002B2_MigrationKindFields` fixture test for 000003
- `db/queries/schema.sql`: smoke query, sqlc input
- `db/queries/workspace.sql`: workspace bootstrap and settings queries (see `workspace` module)
- `internal/testdb/testdb.go`: test-only shared testcontainers Postgres, `NewMigrated`, `Terminate`
- `sqlc.yaml`: sqlc config (pgx/v5, uuid to google/uuid, timestamptz to time.Time)
- `internal/database/sqlc/`: generated code, never edit
- `internal/database/migrations_test.go`: up, down, up cycle and down with data
- `internal/database/schema_test.go`: constraint names, SQLSTATE, D-051 trash FK behavior

## Interfaces

- `NewPool(ctx, databaseURL string, maxConns int32) (*pgxpool.Pool, error)`: parses URL, sets `MaxConns`, pings; closes the pool on ping failure
- `TxBeginner`: `Begin(ctx) (pgx.Tx, error)`; satisfied by `*pgxpool.Pool`
- `InTx(ctx, db TxBeginner, fn func(*sqlc.Queries) error) error`: commits if `fn` returns nil, rolls back on error or panic
- `make migrate`: applies migrations
- `make generate`: runs `go tool sqlc generate`

## Gotchas

- `InTx` re-panics after rolling back. Rollback uses `context.WithoutCancel`, so a cancelled ctx still rolls back. A failed rollback is joined into the returned error.
- Multi-statement writes must go through `InTx` (ARCHITECTURE §2).
- The down migration drops all tables and the pg_trgm extension, so it erases all data.
- Migration 000002 is lossy on down: summary and status come back NULL, split lists stay split, and the default currency returns as EUR. Take a backup first.
- Migration 000003 down is a no-op (`SELECT 1`): make and model stay in the content blocks. 000004 down drops every `pinned_fields`; 000005 down turns `price` sorts into `manual`.
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir (hence `schema.sql`) and on a recursive CTE filtering with a plain `@param` (use `sqlc.arg(x)::uuid`, see `folders.sql`).
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- Migration tests use the `pgx5://` scheme and one testcontainers database per test; `internal/testdb` shares one container per test binary.
