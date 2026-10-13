# httpapi

## Purpose

Holds the HTTP router and its handlers. Today it exposes only the two probe endpoints; `cmd/api` builds the router from injected `Deps`. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/httpapi/router.go`: `NewRouter`, `/healthz` and `/readyz` handlers, writability probe
- `internal/httpapi/router_test.go`: probe tests against real Postgres (testcontainers) and fake pingers
- `cmd/api/main.go`: wires the router and the `healthcheck` subcommand

## Interfaces

- `NewRouter(d Deps) http.Handler`: builds the chi router
- `Deps`: `DB Pinger`, `DataDir string`, `Logger *slog.Logger`
- `Pinger`: `Ping(ctx) error`; satisfied by `*pgxpool.Pool`
- `GET /healthz`: 200 `ok`; checks nothing else
- `GET /readyz`: 200 `ready` when the DB ping (2s timeout) and the DATA_DIR probe both pass
- `GET /readyz`: 503 `database unavailable` if the ping fails (checked first)
- `GET /readyz`: 503 `storage unavailable` if DATA_DIR is not writable

## Gotchas

- `/healthz` is liveness only and ignores the DB and storage. Use `/readyz` for readiness.
- 503 bodies are fixed strings with no paths, SQL or driver text. The underlying error is logged server-side at WARN.
- Every `/readyz` call creates and deletes a `.readyz-*` file in DATA_DIR.
- `/app/api healthcheck` (in `cmd/api`) GETs `/readyz` on `127.0.0.1` at the port from `HTTP_ADDR` (default 8080) with a 3s timeout. It exits 0 only on 200. Compose uses it because the distroless image has no shell or curl.
- No middleware yet; B-04 adds it.
