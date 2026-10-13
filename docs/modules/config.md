# config

## Purpose

Parses and validates the API's environment configuration once at startup. Every problem is collected and returned in one error, so a misconfigured container fails fast. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/config/config.go`: `Config`, `Load`, all validation rules
- `internal/config/config_test.go`: defaults, overrides, validation table, password-leak check

## Interfaces

- `Load(getenv func(string) string) (Config, error)`: pass `os.Getenv` in production
- `Config`: `AppEnv`, `HTTPAddr`, `DatabaseURL`, `DBMaxConns`, `DataDir`, `WorkspaceName`, `LogLevel`, `LogFormat`
- `APP_ENV`: `development` (default) or `production`, case-sensitive
- `HTTP_ADDR`: `host:port` listen address, default `:8080`
- `DATABASE_URL`: required; `postgres://` or `postgresql://` with a host
- `DB_MAX_CONNS`: integer 1 to 100, default 10
- `DATA_DIR`: default `/data`; must be an existing, writable directory
- `WORKSPACE_NAME`: default `Our Collection`
- `LOG_LEVEL`: `debug`, `info` (default), `warn`, `error`
- `LOG_FORMAT`: `json` (default) or `text`

## Gotchas

- Values are trimmed, and an empty or blank value falls back to the default (`DATABASE_URL` has no default, so blank means missing).
- Errors are joined under the prefix `invalid configuration:`, and `cmd/api` prints them as `fatal: ...`.
- Only `DATABASE_URL` is kept out of error text (it carries the password). Other messages echo the raw value, e.g. `LOG_LEVEL` and `DATA_DIR`.
- `Load` has a side effect: it creates and deletes a `.startup-*` probe file in `DATA_DIR` to test writability.
