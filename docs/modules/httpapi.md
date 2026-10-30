# httpapi

## Purpose

Composition root of the HTTP layer. `NewRouter` builds the Huma API over chi, registers the health probes and every feature package's operations, and wraps the router in the shared middleware. `API` returns the same registrations without serving them, and `Spec` renders the OpenAPI document from them (`cmd/specgen`). Shared middleware, Huma and problem mapping live in [platform](platform.md). Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `API`, `build`, `register` (one `Register` call per feature), `Deps`, and the adapters that wire services together
- `internal/httpapi/health.go`: `/healthz` and `/readyz` as Huma operations (text/plain)
- `internal/httpapi/spec.go`: `Spec()`, the document info description and tag list
- `internal/httpapi/spec_test.go`: spec validity, registration match, route coverage
- `cmd/specgen/main.go`: prints `Spec()` for `make spec`

## Interfaces

- `NewRouter(d Deps) (http.Handler, error)`: router behind request id, security headers, access log and recovery
- `API(d Deps) huma.API`: all operations registered, used with a zero `Deps` by `Spec`
- `Spec() ([]byte, error)`: OpenAPI 3.1 YAML of the registered operations
- `GET /healthz`: 200 `ok`, liveness only
- `GET /readyz`: 200 `ready`, or 503 when the DB ping or the `DATA_DIR` probe fails
- `NewItems(pool, clk, settings) *items.Service`: built by `cmd/api`, shared with lookup and images

## Gotchas

- Registration must not dereference a dependency: `Spec` runs with a zero `Deps`.
- `/readyz` creates and deletes a `.readyz-*` file in `DATA_DIR` on every call.
- `folderDB`, `typeDB` and `contentDB` type-assert `Deps.DB` to the pool; a Ping-only fake gets a nil database, so routes that query need a real DB in tests.
- `importer(d)` is nil when lookup is off, so item create takes no lookup path.
- `internal/httpapi` is in archtest `exemptPackages` (composition root), so it may import every feature.
