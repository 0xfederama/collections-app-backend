# httpapi

## Purpose

Serves the legacy generated strict-server routes on chi, with a per-operation guard that enforces rules the generated code skips. Routes migrated to Huma (settings in `workspace`; folders, lists and the tree read in their own packages) are mounted on the same router after them. Shared middleware, Huma and problem mapping live in `internal/platform` (see [platform](platform.md)). Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `Deps`, route order, scope and `workspace.Register` mounting
- `internal/httpapi/server.go`: `Server`, `/healthz` and `/readyz` handlers, DATA_DIR probe
- `internal/httpapi/trash.go`: trash handlers (folder/list routes are Huma in [folders](folders.md), [lists](lists.md), [tree](tree.md); types in [definitions](definitions.md))
- `internal/httpapi/items.go`, `content.go`, `search.go`, `images.go`, `tmdb.go`, `books.go`, `stores.go`: item, content, search, file, TMDB, book and store handlers
- `internal/httpapi/middleware.go`: `IfMatchVersion` only
- `internal/httpapi/guard.go`: query schemas, If-Match, media type, body limits, unknown fields
- `internal/httpapi/problem.go`, `paramerr.go`: legacy problem writer and `toProblem` mapper
- `internal/httpapi/oapi/oapi.gen.go`: generated, never edit (embeds the spec)

## Interfaces

- `NewRouter(d Deps) (http.Handler, error)`: defaults `Clock` to `clock.System{}`; fails if the embedded spec does not load
- `Deps`: domain services (`Books lookup.Provider`, `BookLookupAvailable`, ...) and `WorkspaceID`; `NewRouter` builds the `definitions` repository and the `lists.TypeSource` adapter (`listTypes`)
- `IfMatchVersion(ctx) int`: version N of the validated `If-Match` in guarded handlers
- `GET /healthz`: 200 `ok`, liveness only; `GET /readyz`: 200 `ready` or 503 (DB ping, DATA_DIR probe)
- `/api/v1/types...`: type and custom-field routes are Huma operations in [definitions](definitions.md); see `api/openapi.yaml`
- Delete folder/list/item: 204 with If-Match; trash supports list, restore, single purge and empty
- `GET /api/v1/tmdb/suggestions`, `GET /api/v1/books/suggestions`, `GET /api/v1/store-providers`: suggestions and the fixed store list
- `POST /api/v1/items/{id}/lookup` and item create (`source_id`): explicit lookup with item If-Match

## Gotchas

- `/healthz` ignores the DB and storage. `/readyz` creates and deletes a `.readyz-*` file in DATA_DIR on every call.
- The guard, not the generated server, enforces body limits (413), unknown JSON fields (400 `unknown_field`), missing If-Match (428) and query ranges (400).
- List create and update take 2 MiB JSON bodies (64 KiB Markdown `description`); other JSON bodies keep the 64 KiB limit.
- Folder and list move and reorder bodies must carry the nullable parent field: absent is 422, null means root (generated server for the legacy routes, Huma for folders and lists).
- `folderDB` type-asserts `Deps.DB` to the pool for the folder and list repositories; a Ping-only fake gets a nil database, so folder/list routes need a real DB in tests.
- `toProblem` is the apperr mapper for legacy routes; Huma routes use `platform/problem.From`. Change error mapping in both until B3b..f.
- Attachment responses set `Cache-Control: private, max-age=31536000, immutable` and `Content-Disposition: inline`; nosniff and CSP come from `platform/httpserver`.
- Item metadata keys are checked against database-backed type fields, not by the guard. Item responses add read-only `metadata.store_links` and `metadata.streaming.platform_links`.
- No CORS (D-033); errors outside `apperr` become 500 `internal` with only the request id. Extend `ifMatchPrefix` in `guard.go` by hand for new If-Match operations.
