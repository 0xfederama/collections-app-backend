# logs

## Purpose

Keeps the newest log records of the process in memory and serves them through a small viewer (`LOGS_UI`). Read-only, not part of the public contract. Design context: F-019 "Additional scope" in `/Users/federico/Developer/collections-app/design-docs/features/F-019-backend-architecture.md`.

## Key files

- `internal/platform/logs/buffer.go`: `Buffer` (ring of the newest records, mutex), `NewBuffer`, `Query`, `DefaultSize` (2000)
- `internal/platform/logs/slog.go`: `Handler`, a `slog.Handler` that writes to the normal output and records a copy; redaction (`put`, `value`, `secretKeys`)
- `internal/platform/logs/handler.go`: Huma operations `listLogs` and `viewLogs`, `Register`
- `internal/platform/logs/view.html`: the viewer page, embedded with `go:embed`
- `cmd/api/main.go`: `newLogger` wraps the handler only when `LOGS_UI` is on; `httpapi.Deps.Logs`

## Interfaces

- `GET /admin/logs?level=&since=&limit=`: `{data:[{time,level,msg,attrs}], total}`. `level` is the lowest level (debug, info, warn, error; default all), `since` an RFC 3339 time, `limit` 1 to 2000 (default 200). Oldest first, newest last. `total` counts the matches before the limit.
- `GET /admin/logs/view`: the HTML page, which fetches `../logs`.
- Both operations are `Hidden`: routed, but not in `api/openapi.yaml` (`make check-spec` stays green).

## Gotchas

- Off by default in production. `LOGS_UI` unset means on unless `APP_ENV=production`; `LOGS_UI=true` turns it on there. When off, `Deps.Logs` is nil, the routes are not registered and the answer is the normal 404 problem.
- No auth. The routes share the API listener, so serve them only on the private address (the security review, pool 8, decides exposure).
- Redaction is by exact, case-insensitive key: password, secret, token, api_key, apikey, authorization, cookie, key. It also covers values inside groups with those names. Only the buffer is redacted; stderr writes what the code logged.
- Only logged attributes are kept. Request and response bodies are never logged, so they are never buffered.
- Memory only: a restart empties the buffer. Older history is in `docker logs`.
- A record the output drops (below its level) is not buffered either: `Enabled` follows the output.
- The page sends its own CSP (per-response nonce for its inline style and script, `connect-src 'self'`) instead of the API-wide `default-src 'none'`; see `viewLogs`.
