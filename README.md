# collections-app-backend

Go REST API + PostgreSQL for the Collection App (private, two-person collection manager). It owns the canonical API
contract (`api/openapi.yaml`, arrives in B-03). Design docs: `/Users/federico/Developer/collections-app/design-docs/`.

This repo is the `backend/` submodule of the parent repo [collections-app](https://github.com/0xfederama/collections-app),
next to `frontend-web/` (the React app, repo `collections-app-frontend-web`). From the parent folder,
`docker compose up --build` starts both together. Everything below also works standalone in this repo.

There is no CI. Quality gates run locally with `make check` (see below).

## What exists today (M0 skeleton)

| Piece | State |
|---|---|
| `GET /healthz` | Liveness: always `200 ok` while the process runs |
| `GET /readyz` | Readiness: `200 ready` only if the DB answers and `DATA_DIR` is writable, else a terse `503` (no paths or SQL) |
| Config | Env vars validated at startup, all problems reported together |
| Logging | `log/slog`, `LOG_FORMAT` (`json` or `text`), `LOG_LEVEL` |
| Database | Connection pool, migration `000001_init` (full schema + `pg_trgm`), sqlc-generated code |
| Everything else | Not built yet: contract (B-03), middleware/transactions (B-04), then the domain features (B-05 onward) |

There is no authentication, so the API is published on `127.0.0.1` only. See the parent README, "Security boundary".

## Layout

```text
cmd/api/main.go        wiring only: config → logger → pgx pool → chi router → http.Server (graceful shutdown)
internal/config        env parsing and validation
internal/database      pgx pool constructor
internal/httpapi       router, /healthz, /readyz
db/migrations          golang-migrate files (`000001_init`)
db/queries             sqlc queries (generated into internal/database/sqlc)
scripts/               check-compose-exposure.sh, reset-dev.sh
docs/                  one short doc per module
```

The binary also has a `healthcheck` subcommand (`/app/api healthcheck`) used by the compose healthcheck, because the
distroless runtime image has no shell or curl.

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.27.2 |
| Docker with Compose v2 | any recent (Docker Desktop 28 tested) |
| Git, `gh` | for PRs |

Postgres 18.6 and golang-migrate 4.20.1 run as pinned Docker images, so there's nothing else to install.

## Start

```bash
cp .env.example .env   # set POSTGRES_PASSWORD (URL-safe characters only)
docker compose up --build
```

This starts `db`, runs `migrate` once, then starts `api` on `http://127.0.0.1:8080` (loopback only, never exposed to the LAN).

| Task | Command |
|---|---|
| Start in the background | `docker compose up --build -d` |
| Stop (keep data) | `docker compose down` |
| Logs | `docker compose logs -f api` |
| Health | `curl -fsS http://127.0.0.1:8080/healthz` (liveness), `curl -fsS http://127.0.0.1:8080/readyz` (DB + data dir) |
| Migrate | `make migrate` |
| New migration | `make migration name=<snake_case>` |
| Test / lint / format | `make test` / `make lint` / `make fmt` |
| All gates | `make check` (needs Docker running) |
| Vulnerability scan | `make audit` |
| Safe reset (deletes DB + uploads) | `make reset-dev` (asks first, refuses when `APP_ENV=production`) |

`make check` = gofmt check, golangci-lint (including goimports), `go test ./...` (testcontainers Postgres), a check that compose publishes ports on `127.0.0.1` only, and a Docker image build.

## Host-run loop (faster iteration)

```bash
cp compose.override.example.yaml compose.override.yaml   # publishes 127.0.0.1:5432
docker compose up -d db
make migrate
make run                                                 # uses .env, DATA_DIR=./data
```

`make run` reads `.env` and builds `DATABASE_URL` for `localhost:5432` from `POSTGRES_PASSWORD` (set `DATABASE_URL` in `.env` to override).

## Configuration

Environment variables are validated at startup. See `.env.example` and `design-docs/BACKUP_AND_DEPLOYMENT.md` §3.

## Troubleshooting

- **Tool versions:** `go version` must be 1.27+, `docker compose version` must be v2.
- **Port already in use:** set `API_PORT` in `.env`.
- **`db` exits with "superuser password is not specified":** `POSTGRES_PASSWORD` is empty. Copy `.env.example` to `.env` and set it.
- **DB connection errors:** `docker compose ps` should show `db` healthy. For the host loop, make sure `compose.override.yaml` exists and `DATABASE_URL` (if you set it) uses `localhost`.
- **Migrations not applied:** `make migrate`, then `docker compose logs migrate`. `docker compose up --build` applies 000001 on a fresh database.
- **Storage permissions:** `DATA_DIR` must exist and be writable (the container uses UID 65532 and the `appdata` volume). `/readyz` returns 503 if it isn't.
- **`make check` fails on tests:** Docker must be running. The first lint run compiles golangci-lint and is slow once.
- **Stale generated code:** run `make generate` (sqlc; OpenAPI arrives in B-03) and commit the result. `make check` fails on drift.
