# httpapi

## Purpose

Serves the HTTP API: the generated strict server mounted on chi, behind request id, security headers, access log and panic recovery. A per-operation guard enforces rules the generated code skips. Health ops are implemented; every other operation answers 501 until its task lands. Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `Deps`, middleware order, chi mounting
- `internal/httpapi/server.go`: `Server`, `/healthz` and `/readyz` handlers, DATA_DIR probe
- `internal/httpapi/unimplemented.go`: 501 stubs for all other operations
- `internal/httpapi/middleware.go`: request id, security headers, access log, recovery, `IfMatchVersion`
- `internal/httpapi/guard.go`: query schemas, If-Match, media type, body limits, unknown fields
- `internal/httpapi/problem.go`: problem+json writer, `toProblem` (the only apperr mapper)
- `internal/httpapi/paramerr.go`: binding errors to safe detail text
- `internal/httpapi/etag/etag.go`: `Format` and `Parse` for `"<prefix>-N"`
- `internal/httpapi/oapi/oapi.gen.go`: generated, never edit (embeds the spec)
- `internal/httpapi/*_test.go`, `etag/etag_test.go`: router, handler and guard tests
- `cmd/api/main.go`: wires the router and the `healthcheck` subcommand

## Interfaces

- `NewRouter(d Deps) (http.Handler, error)`: defaults `Clock` to `clock.System{}`; fails if the embedded spec does not load
- `Deps`: `DB Pinger`, `DataDir string`, `Logger *slog.Logger`, `Clock clock.Clock`
- `Pinger`: `Ping(ctx) error`; satisfied by `*pgxpool.Pool`
- `IfMatchVersion(ctx) int`: version N of the validated `If-Match` in guarded handlers
- `GET /healthz`: 200 `ok`, liveness only
- `GET /readyz`: 200 `ready` when the DB ping (2s) and DATA_DIR probe pass
- `GET /readyz`: 503 `database unavailable` (checked first) or `storage unavailable`
- Unknown route or method: 404 `not_found` / 405 `method_not_allowed` problem
- Any other operation: 501 `not_implemented` problem

## Gotchas

- `/healthz` ignores the DB and storage. Use `/readyz` for readiness.
- 503 bodies are fixed strings; driver errors are logged server-side at WARN.
- Every `/readyz` call creates and deletes a `.readyz-*` file in DATA_DIR.
- `/app/api healthcheck` GETs `/readyz` on `127.0.0.1` (port from `HTTP_ADDR`) and exits 0 only on 200.
- The guard, not the generated server, enforces: body limits (413; 64 KiB JSON, 2 MiB content, 10 MiB + 64 KiB multipart), unknown JSON fields (400 `unknown_field`), missing If-Match (428), and query schema ranges (400).
- Errors outside `apperr` become 500 `internal` with only the request id; the stack goes to the log.
- Access log has no bodies or query string; probe routes log at DEBUG.
- No CORS (D-033). Decisions D-055 to D-057 are in `/Users/federico/Developer/collections-app/design-docs/DECISIONS.md`.
