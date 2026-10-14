# httpapi

## Purpose

Serves the HTTP API: the generated strict server mounted on chi, behind request id, security headers, access log and panic recovery. A per-operation guard enforces rules the generated code skips. Every operation in the spec is implemented. Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `Deps`, middleware order, chi mounting
- `internal/httpapi/server.go`: `Server`, `/healthz` and `/readyz` handlers, DATA_DIR probe
- `internal/httpapi/settings.go`, `tree.go`, `folders.go`, `lists.go`: settings, tree, folder and list handlers
- `internal/httpapi/items.go`, `content.go`, `search.go`, `images.go`: item, content, search and file handlers
- `internal/httpapi/trash.go`: DELETE folder/list/item, trash list, restore and purge handlers
- `internal/httpapi/middleware.go`: request id, security headers, access log, recovery, `IfMatchVersion`
- `internal/httpapi/guard.go`: query schemas, If-Match, media type, body limits, unknown fields
- `internal/httpapi/problem.go`, `paramerr.go`: problem+json writer, `toProblem` (the only apperr mapper)
- `internal/httpapi/etag/etag.go`: `Format` and `Parse` for `"<prefix>-N"`
- `internal/httpapi/oapi/oapi.gen.go`: generated, never edit (embeds the spec)
- `cmd/api/main.go`: wires the router and the `healthcheck` subcommand

## Interfaces

- `NewRouter(d Deps) (http.Handler, error)`: defaults `Clock` to `clock.System{}`; fails if the embedded spec does not load
- `Deps`: `DB`, `DataDir`, `Logger`, `Clock`, `WorkspaceID`, `Settings`, `Tree`, `Items`, `Content`, `Search`, `Images`, `Trash`
- `IfMatchVersion(ctx) int`: version N of the validated `If-Match` in guarded handlers
- `GET /healthz`: 200 `ok`, liveness only; `GET /readyz`: 200 `ready` or 503 (DB ping, DATA_DIR probe)
- Settings, tree, folder, list, item, content, search and attachment routes: see `api/openapi.yaml`
- `DELETE /api/v1/folders/{id}`, `DELETE /api/v1/lists/{id}`, `DELETE /api/v1/items/{id}`: 204, If-Match required
- `GET /api/v1/trash`, `POST /api/v1/trash/{batch_id}/restore`, `DELETE /api/v1/trash/{batch_id}`
- Unknown route or method: 404 `not_found` / 405 `method_not_allowed` problem

## Gotchas

- `/healthz` ignores the DB and storage. Use `/readyz`; every `/readyz` call creates and deletes a `.readyz-*` file in DATA_DIR.
- The guard, not the generated server, enforces: body limits (413), unknown JSON fields (400 `unknown_field`), missing If-Match (428), and query schema ranges (400).
- Move and reorder bodies must carry the nullable parent field (`parent_folder_id` or `folder_id`): absent is 422, null means root.
- Attachment responses set `Cache-Control: private, max-age=31536000, immutable` and `Content-Disposition: inline`; nosniff and CSP come from middleware.
- Item `metadata` keys are checked by `items/kinds`, not the guard.
- Errors outside `apperr` become 500 `internal` with only the request id; the stack goes to the log.
- `ifMatchPrefix` in `guard.go` must be extended by hand for any new operation that requires If-Match.
- No CORS (D-033). Decisions D-055 to D-057 are in `/Users/federico/Developer/collections-app/design-docs/DECISIONS.md`.
