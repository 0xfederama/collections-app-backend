# platform

## Purpose

Shared infrastructure under `internal/platform/`, used by feature packages and never by domain rules: database pool and sqlc output, config, clock, ETags, the Huma API on chi, problem+json mapping, the workspace scope on the context, and test helpers. Layer rules (handler, service, repository, database) are enforced by golangci depguard and `internal/platform/archtest`.

## Key files

- `internal/platform/database/`: pool, `InTx`, migration tests; `sqlc/` generated (see [database](database.md))
- `internal/platform/config/`: env parsing and validation (see [config](config.md))
- `internal/platform/clock/clock.go`: `Clock` interface and `System`
- `internal/platform/etag/etag.go`: `Format`/`Parse` for `"<prefix>-N"`, prefixes incl. `Settings = "s"`
- `internal/platform/httpserver/`: `NewAPI` (Huma on chi), `Op` error wrapper, `Wrap` middleware, `Optional[T]`
- `internal/platform/problem/problem.go`: `From` (apperr to problem), Huma error `Install`, request id in ctx
- `internal/platform/scope/scope.go`: `With`, `WorkspaceID`, `Middleware`
- `internal/platform/testdb/testdb.go`: test-only shared Postgres container (see [database](database.md))
- `internal/platform/archtest/layers_test.go`: layer and cross-feature import rules

## Interfaces

- `httpserver.NewAPI(r chi.Router) huma.API`: installs `problem.Install()` and the default body limit (64 KiB)
- `httpserver.Op[I, O](log, fn)`: wraps a handler so its error is answered as a problem; logs unexpected errors
- `httpserver.Wrap(h, log) http.Handler`: request id, security headers, access log, panic recovery
- `httpserver.Optional[T]`: PATCH field with `Set` and `Value`; absent vs explicit null
- `problem.From(err, requestID) (*Problem, unexpected bool)`: maps `apperr` and Huma errors
- `scope.With(ctx, id)`, `scope.WorkspaceID(ctx)`: `uuid.Nil` on an unscoped context

## Gotchas

- `problem.Install` replaces Huma's package-level error constructors once per process (a deliberate global).
- Register Huma operations through `httpserver.Op`, or unexpected errors are not logged.
- `Optional` fields need `json:",omitempty"` so Huma does not mark them required.
- The schema namer strips the `JSON` suffix (`SettingsJSON` becomes `Settings`).
- Huma's own default body size (1 MiB) is mapped to 64 KiB; declaring exactly 1 MiB gets 64 KiB too.
- Until the legacy routes move (B3b..f), `httpapi/problem.go` `toProblem` is a second apperr mapper: change error mapping in both.
- `archtest` exempts `legacyPackages`; remove an entry when its package migrates and keep `.golangci.yml` depguard in step.
