# database

## Purpose

Owns the PostgreSQL layer: the pgx connection pool, the SQL migrations that create the full schema, and sqlc-generated query code. Schema intent lives in `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/database/database.go`: `NewPool`, pool size and startup ping
- `db/migrations/000001_init.up.sql`: pg_trgm extension and the 9 tables
- `db/migrations/000001_init.down.sql`: drops everything (destructive)
- `db/queries/schema.sql`: smoke query, sqlc input
- `sqlc.yaml`: sqlc config (pgx/v5, uuid to google/uuid, timestamptz to time.Time)
- `internal/database/sqlc/`: generated code, never edit
- `internal/database/migrations_test.go`: up, down, up cycle and down with data
- `internal/database/schema_test.go`: constraint names, SQLSTATE, D-051 trash FK behavior

## Interfaces

- `NewPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error)`: parses URL, sets `MaxConns`, pings; closes the pool on ping failure
- `make migrate`: applies migrations
- `make generate`: runs `go tool sqlc generate`

## Gotchas

- The down migration drops all tables and the pg_trgm extension, so it erases all data.
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir, which is why `schema.sql` exists.
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- Migration tests use the `pgx5://` scheme (golang-migrate pgx/v5 driver) and a testcontainers Postgres with one database per test.
