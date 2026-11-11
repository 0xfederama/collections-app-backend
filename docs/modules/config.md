# config

## Purpose

Parses and validates the API's environment configuration once at startup. Every problem is collected and returned in one error, so a misconfigured container fails fast. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/platform/config/config.go`: `Config`, `Load`, all validation rules, `validateIdentity`
- `internal/platform/config/config_test.go`: defaults, overrides, validation table, identity tests, password-leak check

## Interfaces

- `Load(getenv func(string) string) (Config, error)`: pass `os.Getenv` in production
- `Config`: `AppEnv`, `HTTPAddr`, `DatabaseURL`, `DBMaxConns`, `DataDir`, `WorkspaceName`, optional `TMDBAPIKey`, `TMDBMockFixture`, optional `OpenLibraryContact`, optional `HardcoverAPIKey`, `LogLevel`, `LogFormat`, `LogsUI`, `AccessTeamDomain`, `AccessAUD`, `OwnerEmail`, `LANOwnerFallback`, `DevIdentity`
- `(Config).DevIdentities() []string`: owner (`OWNER_EMAIL` or `owner@dev.test`), then `alice@dev.test`, `bob@dev.test`
- `APP_ENV`: `development` (default) or `production`, case-sensitive
- `HTTP_ADDR`: `host:port` listen address, default `:8080`
- `DATABASE_URL`: required; `postgres://` or `postgresql://` with a host
- `DB_MAX_CONNS`: integer 1 to 100, default 10
- `DATA_DIR`: default `/data`; must be an existing, writable directory
- `WORKSPACE_NAME`: default `Collections`; used only when the workspace is first created
- `TMDB_API_KEY`, `TMDB_MOCK_FIXTURE`, `OPENLIBRARY_CONTACT`, `HARDCOVER_API_KEY`: see [lookup](lookup.md), [tmdb](tmdb.md), [openlibrary](openlibrary.md), [hardcover](hardcover.md)
- `LOG_LEVEL`, `LOG_FORMAT`, `LOGS_UI`: see [logs](logs.md)
- `ACCESS_TEAM_DOMAIN`: Cloudflare Access team host (e.g. `myteam.cloudflareaccess.com`); a leading `https://` and trailing `/` are stripped; must contain a dot
- `ACCESS_AUD`: the Access application's audience tag; set together with `ACCESS_TEAM_DOMAIN` or not at all
- `OWNER_EMAIL`: owner e-mail, lower-cased; required by `LAN_OWNER_FALLBACK`
- `LAN_OWNER_FALLBACK`: `true`/`false`, default false; emergency switch (see [identity](identity.md))
- `DEV_IDENTITY`: `true`/`false`, default false; enables the dev identities

## Gotchas

- Production (`APP_ENV=production`) requires `ACCESS_TEAM_DOMAIN` and `ACCESS_AUD`, and refuses `DEV_IDENTITY=true`. `TMDB_MOCK_FIXTURE` is refused too.
- Development needs one identity provider: Access, `DEV_IDENTITY=true`, or `LAN_OWNER_FALLBACK=true`; none means startup fails.
- `LAN_OWNER_FALLBACK=true` without `OWNER_EMAIL` is refused. `ACCESS_TEAM_DOMAIN` without `ACCESS_AUD` (or the reverse) is refused.
- Values are trimmed, and an empty or blank value falls back to the default (`DATABASE_URL` has no default, so blank means missing).
- Errors are joined under the prefix `invalid configuration:`, and `cmd/api` prints them as `fatal: ...`.
- Only `DATABASE_URL` is kept out of error text (it carries the password). Other messages echo the raw value.
- `Load` has a side effect: it creates and deletes a `.startup-*` probe file in `DATA_DIR` to test writability.
- `compose.yaml` passes the identity variables with defaults `DEV_IDENTITY=true` and `LAN_OWNER_FALLBACK=false`; production sets the Access values in its own environment.
- Keep API keys in the server environment (`deploy/server/.env`); never expose them to the browser.
