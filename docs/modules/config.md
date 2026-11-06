# config

## Purpose

Parses and validates the API's environment configuration once at startup. Every problem is collected and returned in one error, so a misconfigured container fails fast. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/platform/config/config.go`: `Config`, `Load`, all validation rules
- `internal/platform/config/config_test.go`: defaults, overrides, validation table, password-leak check

## Interfaces

- `Load(getenv func(string) string) (Config, error)`: pass `os.Getenv` in production
- `Config`: `AppEnv`, `HTTPAddr`, `DatabaseURL`, `DBMaxConns`, `DataDir`, `WorkspaceName`, optional `TMDBAPIKey`, `TMDBMockFixture`, optional `OpenLibraryContact`, optional `HardcoverAPIKey`, `LogLevel`, `LogFormat`, `LogsUI`
- `APP_ENV`: `development` (default) or `production`, case-sensitive
- `HTTP_ADDR`: `host:port` listen address, default `:8080`
- `DATABASE_URL`: required; `postgres://` or `postgresql://` with a host
- `DB_MAX_CONNS`: integer 1 to 100, default 10
- `DATA_DIR`: default `/data`; must be an existing, writable directory
- `WORKSPACE_NAME`: default `Collections`; used only when the workspace is first created (bootstrap never renames an existing one)
- `TMDB_API_KEY`: optional server-side TMDB credential; blank disables lookup
- `TMDB_MOCK_FIXTURE`: `true`/`false`, default false; test stacks only (rejected when `APP_ENV=production`); also enables the `[mock] book name` Open Library fixture
- `HARDCOVER_API_KEY`: optional Hardcover token (gap-filler of the automatic book lookup); blank disables it, never required, never logged
- `LOG_LEVEL`: `debug`, `info` (default), `warn`, `error`
- `LOG_FORMAT`: `json` (default) or `text`
- `LOGS_UI`: `true`/`false`; default on unless `APP_ENV=production`. Serves the in-memory log viewer (see [logs](logs.md)); no auth, keep it on the private address

## Gotchas

- Values are trimmed, and an empty or blank value falls back to the default (`DATABASE_URL` has no default, so blank means missing).
- Errors are joined under the prefix `invalid configuration:`, and `cmd/api` prints them as `fatal: ...`.
- Only `DATABASE_URL` is kept out of error text (it carries the password). Other messages echo the raw value, e.g. `LOG_LEVEL` and `DATA_DIR`.
- `Load` has a side effect: it creates and deletes a `.startup-*` probe file in `DATA_DIR` to test writability.
- Keep `TMDB_API_KEY` in the server environment (`deploy/server/.env`); never expose it to the browser. Get a key through [TMDB account API settings](https://www.themoviedb.org/settings/api).
- `OPENLIBRARY_CONTACT`: optional e-mail for the Open Library User-Agent (faster pacing). Book lookup (Open Library) is always on, no key. `TMDB_MOCK_FIXTURE` serves the `[mock] book name` fixture only; every other book query reaches Open Library.
- `HARDCOVER_API_KEY` is read like `TMDB_API_KEY` but is not enabled by `TMDB_MOCK_FIXTURE`; keep it server side (see [hardcover](hardcover.md)).
