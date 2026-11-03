# database

## Purpose

Owns the PostgreSQL layer: connection pool, transaction helper, migrations, and sqlc-generated queries. The baseline migration is pure schema (no triggers, functions or ON DELETE CASCADE); the built-in types are Go code (`internal/items/types/registry.go`), not rows. Schema intent: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md`.

## Key files

- `internal/platform/database/database.go`: `NewPool`, `TxBeginner`, `InTx`
- `internal/platform/database/intx_test.go`: commit, rollback, panic and `NewPool` tests
- `db/migrations/000001_init.{up,down}.sql`: the baseline schema (pg_trgm, all tables, no logic); down drops everything. Later changes append as new numbered files
- `db/queries/items.sql`, `content.sql`: item row reads and writes (one query per repository method, list filtering in Go) and content locks
- `db/queries/trash.sql`, `search.sql`: trash batch and purge queries, search and recent reads (see [trash](trash.md), [search](search.md))
- `db/queries/schema.sql`: smoke query, sqlc input
- `db/queries/workspace.sql`: workspace bootstrap and settings queries (see `workspace` module)
- `db/queries/lookup_imports.sql`: lookup provenance queries (see [lookup](lookup.md))
- `db/migrations.go`: package `db`, embeds `migrations/*.sql` (`db.Migrations()`); the Dockerfile copies `db/`
- `internal/platform/database/migrate.go`: `Migrate`, used by `api migrate` and by `testdb`
- `internal/platform/testdb/testdb.go`: test-only shared testcontainers Postgres, `NewMigrated` (same embedded migrations as the binary), `Terminate`
- `sqlc.yaml`: sqlc config (pgx/v5, uuid to google/uuid, timestamptz to time.Time)
- `internal/platform/database/sqlc/`: generated code, never edit (`sqlc.yaml` output)
- `internal/platform/database/migrations_test.go`, `schema_test.go`: up/down/up cycle, constraint names, SQLSTATE, D-051

## Interfaces

- `NewPool(ctx, databaseURL string, maxConns int32) (*pgxpool.Pool, error)`: parses URL, sets `MaxConns`, pings; closes the pool on ping failure
- `TxBeginner`: `Begin(ctx) (pgx.Tx, error)`; satisfied by `*pgxpool.Pool`
- `InTx(ctx, db TxBeginner, fn func(*sqlc.Queries) error) error`: commits if `fn` returns nil, rolls back on error or panic
- `Migrate(fsys fs.FS, dir, databaseURL string) (uint, error)`: applies pending migrations through golang-migrate (`schema_migrations` table, compatible with the migrate CLI), returns the version; no change is not an error, a dirty state is
- `/app/api migrate`: reads only `DATABASE_URL`, applies migrations, exits 0 without starting the server; the server never migrates at startup
- `make migrate`: runs the compose `migrate` service (the API image with `migrate`); `make generate`: runs `go tool sqlc generate`

## Gotchas

- `InTx` re-panics after rolling back. Rollback uses `context.WithoutCancel`, so a cancelled ctx still rolls back. A failed rollback is joined into the returned error.
- Multi-statement writes must go through `InTx` (ARCHITECTURE §2).
- Only a feature's `repository.go` may import sqlc (archtest `sqlc is imported only by repository.go`, with a short exempt list). Services reach the database through their own `Repo` interface.
- Migration tests read `file://../../../db/migrations` (from `internal/platform/database`).
- `make check-generate` fails if regenerated files differ from the committed ones; run `make generate` and commit.
- sqlc errors on an empty `db/queries` dir (hence `schema.sql`). A recursive CTE filtering on a plain `@param` needs `sqlc.arg(x)::uuid`; none remain, trash walks run in Go (`internal/trash/walk.go`).
- Constraint names follow `<table>_<cols>_{pkey,key,fkey,check,idx}`; tests assert on them.
- No ON DELETE CASCADE and no triggers in the schema (`schema_test.go` asserts both): code deletes children explicitly, children first (D-051).
- Migration tests use the `pgx5://` scheme and one testcontainers database per test; `internal/platform/testdb` shares one container per test binary.
