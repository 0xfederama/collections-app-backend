# database

## Purpose

Owns the PostgreSQL layer: connection pool, transaction helper, migrations, and sqlc-generated queries. The baseline migration holds the full schema, including the seeded built-in types. Schema intent: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/database/database.go`: `NewPool`, `TxBeginner`, `InTx`
- `internal/database/intx_test.go`: commit, rollback, panic and `NewPool` tests
- `db/migrations/000001_init.{up,down}.sql`: the baseline schema (pg_trgm, all tables, seed functions, trigger); down drops everything. Later changes append as new numbered files
- `db/queries/types.sql`: definition reads, locks, writes and cleanup queries
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
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir (hence `schema.sql`) and on a recursive CTE filtering with a plain `@param` (use `sqlc.arg(x)::uuid`, see `folders.sql`).
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- Migration tests use the `pgx5://` scheme and one testcontainers database per test; `internal/testdb` shares one container per test binary.
