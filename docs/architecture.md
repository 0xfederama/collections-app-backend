# Architecture

Package-level map of the backend. Design rationale: `/Users/federico/Developer/collections-app/design-docs/features/F-019-backend-architecture.md`.

## Layers

```text
HTTP -> handler.go -> service.go -> repository.go -> platform/database (pgx, sqlc)
cmd/api wires the pool, services and router; httpapi is the composition root (register per feature)
platform/*: httpserver, problem, scope, clock, etag, config, logs (used by features, never by rules)
integrations/*: tmdb, openlibrary, hardcover (bounded clients, used by lookup only)
```

- `handler.go`: Huma operations only (`huma.Register` + `httpserver.Op`), no pgx, no sqlc.
- `service.go`: domain rules, no SQL, no HTTP; reaches data through its `Repo`/`Store` interface.
- `repository.go`: the only file that runs queries (sqlc). Enforced by `.golangci.yml` depguard and `internal/platform/archtest`.
- `httpapi` and `cmd/api` are exempt (`exemptPackages` in `archtest/layers_test.go`); no feature is.

## Rules

- **Package by feature.** `internal/<feature>` owns its handler, service, repository, wire types and tests.
- **Small consumer-declared interfaces.** A service declares the interface it needs (`Repo`, `Store`, `DB`); `httpapi` passes concrete types.
- **Workspace from context.** `scope.Middleware` sets the workspace ID; repositories read `scope.WorkspaceID(ctx)`. Services never take it from the request.
- **Tree lock.** Structural writes (folder/list/item create, move, reorder, trash) call `LockTree` inside their tx (D-060).
- **Files before/after the tx.** Write files before the transaction, delete them after commit (`images`, trash purge).
- **Explicit deletes.** Soft delete into trash batches; hard deletes only in named purge functions, never business logic in `ON DELETE CASCADE`.
- **Generated contract.** `api/openapi.yaml` is generated from the Huma operations (`make spec`); `make check-spec` fails on drift. sqlc output is never edited (`make generate`).

## Adding an endpoint

1. Wire types in the feature's `wire.go` (Huma input/output structs, `Optional[T]` for PATCH).
2. Handler in `handler.go`: `huma.Register(api, huma.Operation{...}, httpserver.Op(log, h.method))`.
3. Service rule in `service.go`; return `apperr` errors, never HTTP status codes.
4. Repository method in `repository.go` backed by a query in `db/queries/*.sql`, then `make generate`.
5. Register the feature's `Register` call in `internal/httpapi/router.go` (`register`), if the feature is new.
6. Run `make spec` and commit `api/openapi.yaml` with the code.
7. Tests: handler and service tests against real Postgres (`internal/platform/testdb`), named after the acceptance test.

## Module docs

- Index: [README.md](README.md)
- Shared plumbing: [platform](modules/platform.md), [httpapi](modules/httpapi.md), [apperr](modules/apperr.md), [database](modules/database.md)
- Features: [items](modules/items.md), [lists](modules/lists.md), [folders](modules/folders.md), [trash](modules/trash.md), [images](modules/images.md), [lookup](modules/lookup.md)
