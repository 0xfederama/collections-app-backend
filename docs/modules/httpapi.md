# httpapi

## Purpose

Composition root of the HTTP layer. It serves only `/healthz` and `/readyz` through the legacy generated strict server on chi, with the guard and problem scaffolding that B5 removes. Every other `/api/v1` operation is a Huma registration made by its feature package and mounted on the same router. Shared middleware, Huma and problem mapping live in `internal/platform` (see [platform](platform.md)). Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `Deps`, the legacy-then-Huma mounting order, and the `Register` calls for every feature package
- `internal/httpapi/server.go`: `Server` with the `/healthz` and `/readyz` handlers and the DATA_DIR probe
- `internal/httpapi/guard.go`: query ranges, body limits and unknown-field checks for the legacy routes
- `internal/httpapi/problem.go`, `paramerr.go`: legacy problem writer and `toProblem` mapper
- `internal/httpapi/oapi/oapi.gen.go`: generated, never edit (embeds the spec)
- Feature routes: [lookup](lookup.md), [stores](stores.md), [items](items.md), [images](images.md), [trash](trash.md), [search](search.md), [folders](folders.md), [lists](lists.md), [tree](tree.md), [definitions](definitions.md), [content](content.md), [workspace](workspace.md)

## Interfaces

- `NewRouter(d Deps) (http.Handler, error)`: defaults `Clock` to `clock.System{}`; fails if the embedded spec does not load
- `Deps`: `DB`, `DataDir`, `Logger`, `WorkspaceID`, `Settings`, the lookup/items/images/trash/search services and the TMDB and book providers
- `NewItems(pool, clk, settings) *items.Service`: built by `cmd/api`, shared with lookup and images
- `GET /healthz`: 200 `ok`, liveness only. `GET /readyz`: 200 `ready` or 503 (DB ping, DATA_DIR probe)

## Gotchas

- `/healthz` ignores the DB and storage. `/readyz` creates and deletes a `.readyz-*` file in DATA_DIR on every call.
- The guard enforces body limits (413), unknown JSON fields (400 `unknown_field`) and query ranges (400) for legacy routes only. Huma operations carry their own checks.
- Huma routes registered after the legacy ones replace them on the same method and path.
- `folderDB`, `typeDB` and `contentDB` type-assert `Deps.DB` to the pool; a Ping-only fake gets a nil database, so routes that query need a real DB in tests.
- `toProblem` is the apperr mapper for legacy routes; Huma routes use `platform/problem.From`. Change error mapping in both until B5.
- `importer(d)` is nil when lookup is off, so item create takes no lookup path.
- No CORS (D-033); errors outside `apperr` become 500 `internal` with only the request id.
