# collections-app-backend

Go REST API + PostgreSQL for the Collection App (private, two-person collection manager). It owns the canonical API
contract (`api/openapi.yaml`, OpenAPI 3.0.3). Design docs: `/Users/federico/Developer/collections-app/design-docs/`.

This repo is the `backend/` submodule of the parent repo [collections-app](https://github.com/0xfederama/collections-app),
next to `frontend-web/` (the React app, repo `collections-app-frontend-web`). From the parent folder,
`./deploy/dev up -d` starts both together (see the parent README). Everything below also works standalone in this repo.

There is no CI. Quality gates run locally with `make check` (see below).

## What exists today

The whole API in `api/openapi.yaml` is implemented (spec `0.3.0`, tag `api-v0.3.0`):

| Area | State |
|---|---|
| Health | `GET /healthz` liveness (`200 ok`), `GET /readyz` readiness (`200 ready` only if the DB answers and `DATA_DIR` is writable, else a terse `503`, no paths or SQL) |
| Data | Settings, tree, folders, lists, items (six kinds), Markdown content with versions, one image per item, search, recent items, trash with restore and purge |
| Cross-cutting | Request IDs, access log, panic recovery, security headers, request guard (`If-Match`, body limits, unknown fields), problem+json errors |
| Operations | Config validated at startup, `log/slog`, migrations, backup/restore/verify scripts |

There is no authentication, so the API is published on `127.0.0.1` only. See the parent README, "Security boundary".

## Layout

```text
cmd/api/main.go        wiring only: config → logger → pgx pool → services → chi router → http.Server (graceful shutdown)
internal/config        env parsing and validation
internal/database      pgx pool, InTx; sqlc/ is GENERATED (never edit)
internal/apperr        typed domain errors (no HTTP)
internal/tree          folders and lists
internal/items         items; items/kinds validates per-kind metadata
internal/content       Markdown content and versions
internal/images        upload validation, thumbnails, serving, orphan sweeper
internal/storage       FileStore (local files)
internal/trash         delete, list, restore, purge
internal/search        search and recent items
internal/workspace     bootstrap of the single workspace
internal/httpapi       router, handlers, middleware, problem mapping; oapi/ is GENERATED from api/openapi.yaml (never edit)
api/openapi.yaml       the canonical API contract (spec wins over design-docs/API.md)
db/migrations          golang-migrate files
db/queries             sqlc queries (generated into internal/database/sqlc)
scripts/               backup, restore, verify, compose exposure check, reset-dev
docs/                  one short doc per module
```

The binary also has a `healthcheck` subcommand (`/app/api healthcheck`) used by the compose healthcheck, because the
distroless runtime image has no shell or curl, and a `strip-images` subcommand (`/app/api strip-images`, see below).

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.27.2 |
| Docker with Compose v2 | any recent (Docker Desktop 28 tested) |
| Node | 24+ (for `make lint-api`, which runs Redocly through `npx`; `make check` includes it, so it is needed for the gate) |
| Git, `gh` | for PRs |
| `jq`, optionally `zstd` | for the backup scripts |

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
| Lint the API spec | `make lint-api` (Redocly, config in `redocly.yaml`) |
| Regenerate code | `make generate` (sqlc + oapi-codegen) |
| All gates | `make check` (needs Docker running) |
| Vulnerability scan | `make audit` |
| Safe reset (deletes DB + uploads) | `make reset-dev` (asks first, refuses when `APP_ENV=production`) |
| Backup / restore / verify | `scripts/backup.sh`, `scripts/restore.sh`, `scripts/verify-restore.sh` (see [Backup and restore](#backup-and-restore)) |
| Backup round-trip test (AT-17) | `make test-backup` (about a minute, not part of `make check`) |
| API smoke test | `make smoke` (curl + jq against a running stack, `API_URL` defaults to `http://localhost:8090`, not part of `make check`) |

`make check` = gofmt check, golangci-lint (including goimports), the Redocly spec lint, shellcheck on `scripts/` (pinned Docker image), a generated-code drift check, `go test ./...` (testcontainers Postgres), a check that compose publishes ports on `127.0.0.1` only, and a Docker image build.

Run `make smoke` after a deploy, a compose change or a migration change, and before a release. It walks one end-to-end flow over HTTP (folder, list, item, image, sort, search, trash, restore, purge) and removes what it created. Point it at the test stack, never at dev data: `./deploy/test up -d --fresh`, then `make smoke` (or `API_URL=http://host:port make smoke`).

## Backup and restore

The data lives in two places, the Postgres volume and the uploaded images (`appdata` volume). A backup always takes both; a database-only backup is not enough. Needs Docker Compose, `jq` and (preferably) `zstd` on the host (without `zstd`, `gzip` is used).

```bash
scripts/backup.sh                    # -> ./backups/<UTC timestamp>/{db.dump,files.tar.zst,manifest.json}
scripts/backup.sh /mnt/usb/backups   # another destination
scripts/verify-restore.sh ./backups/<timestamp>
scripts/restore.sh ./backups/<timestamp>           # into EMPTY volumes
scripts/restore.sh ./backups/<timestamp> --force   # deletes the current data first (asks you to type "restore")
```

- `backup.sh` stops the `api` for a few seconds so nothing is written while it copies, and always starts it again, even when a step fails. A failed run leaves no half-written backup behind.
- `restore.sh` checks the checksums in the manifest, restores the database and the images, verifies the result, applies any newer migrations and starts the `api`. It refuses non-empty volumes unless you pass `--force`.
- `verify-restore.sh` compares row counts per table, the latest Markdown body of every item and every attachment (original and thumbnail present, original checksum matching) with the manifest, and exits non-zero with a list of what differs. Run it right after a restore: edits made since the backup show up as differences.
- Run the scripts from any directory; set `COMPOSE_PROJECT_NAME` if your stack uses a non-default project name.
- **Back up before every migration in production** (upgrade steps: backup, pull new images, `migrate up`, restart, check `/readyz`). To roll back, restore the backup taken before the upgrade: a migration `down` does not bring the files back and can lose data.
- **Retention:** keep at least 7 daily and 4 weekly copies **off the device** (another disk, NAS or cloud of your choice). Backups hold all your data and the images unencrypted; keep them private and never commit them (`backups/` is gitignored).
- Only restore backups you made yourself: the manifest checksums detect corruption, not a malicious archive.

## Strip metadata from existing images

New uploads are stored without EXIF, GPS, XMP, IPTC or comment metadata (the pixels are not re-encoded; a JPEG keeps only its orientation tag, and ICC colour profiles stay). Images uploaded before that still carry it. To clean them:

```bash
docker compose exec api /app/api strip-images   # or: make strip-images
./deploy/prod exec api /app/api strip-images    # prod stack, run from the parent repo
```

It reports `total`, `stripped`, `already clean`, `skipped` (missing or unparseable files, left untouched) and `failed` (I/O or database errors; the exit code is non-zero only then). It never deletes a file, updates `size_bytes` and `sha256` of each rewritten original, and is safe to re-run: a second run strips nothing. Take a backup first if you want a way back.

## Host-run loop (faster iteration)

```bash
cp compose.override.example.yaml compose.override.yaml   # publishes 127.0.0.1:5432
docker compose up -d db
make migrate
make run                                                 # uses .env, DATA_DIR=./data
```

`make run` reads `.env` and builds `DATABASE_URL` for `localhost:5432` from `POSTGRES_PASSWORD` (set `DATABASE_URL` in `.env` to override).

## API contract

`api/openapi.yaml` is the source of truth for every endpoint, schema, header and error. Change the spec first, then run
`make generate` to refresh `internal/httpapi/oapi/oapi.gen.go` (committed, never hand-edited), and commit both. `make check` lints the
spec with Redocly and fails if the generated code is out of date. The web repo vendors a pinned copy of the spec
(`api/openapi.lock.json` there); contract changes follow `design-docs/AGENT_WORKFLOW.md` §6.

## Configuration

Environment variables are validated at startup. See `.env.example` and `design-docs/BACKUP_AND_DEPLOYMENT.md` §3. TMDB lookup is optional: request a key in [TMDB account API settings](https://www.themoviedb.org/settings/api) and set `TMDB_API_KEY` in the parent repo's `deploy/server/.env`; keep it server-side. A missing key disables lookup, while item creation keeps the typed title. The app must show an [approved TMDB logo and attribution](https://www.themoviedb.org/about/logos-attribution) in About; [TMDB watch-provider results](https://developer.themoviedb.org/reference/tv-series-watch-providers) require JustWatch attribution. Imported metadata and posters expire within six months, preserving user edits. Backups containing TMDB data must also be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use); production backup retention is shorter.

## Troubleshooting

- **Tool versions:** `go version` must be 1.27+, `docker compose version` must be v2.
- **Port already in use:** set `API_PORT` in `.env`.
- **`db` exits with "superuser password is not specified":** `POSTGRES_PASSWORD` is empty. Copy `.env.example` to `.env` and set it.
- **DB connection errors:** `docker compose ps` should show `db` healthy. For the host loop, make sure `compose.override.yaml` exists and `DATABASE_URL` (if you set it) uses `localhost`.
- **Migrations not applied:** `make migrate`, then `docker compose logs migrate`. `docker compose up --build` applies 000001 on a fresh database.
- **Storage permissions:** `DATA_DIR` must exist and be writable (the container uses UID 65532 and the `appdata` volume). `/readyz` returns 503 if it isn't.
- **`make check` fails on tests:** Docker must be running. The first lint run compiles golangci-lint and is slow once.
- **Stale generated code:** run `make generate` (sqlc and oapi-codegen) and commit the result. `make check` fails on drift.
