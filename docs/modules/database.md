# database

## Purpose

Owns the PostgreSQL layer: the pgx connection pool, the transaction helper, the SQL migrations that create the full schema, and sqlc-generated query code. Schema intent lives in `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/database/database.go`: `NewPool`, `TxBeginner`, `InTx`
- `internal/database/intx_test.go`: commit, rollback, panic and `NewPool` tests
- `db/migrations/000001_init.up.sql`: pg_trgm extension and the 8 tables
- `db/migrations/000001_init.down.sql`: drops everything (destructive)
- `db/queries/schema.sql`: smoke query, sqlc input
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
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir, which is why `schema.sql` exists.
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- Migration tests use the `pgx5://` scheme and a testcontainers Postgres with one database per test.
