# collections-app-backend

Go REST API + PostgreSQL for the Collection App (private, two-person collection manager). It owns the canonical API
contract (`api/openapi.yaml`, OpenAPI 3.1, generated from the Huma handlers with `make spec`). Design docs: `/Users/federico/Developer/collections-app/design-docs/`.

This repo is the `backend/` submodule of the parent repo [collections-app](https://github.com/0xfederama/collections-app),
next to `frontend-web/` (the React app, repo `collections-app-frontend-web`). From the parent folder,
`./deploy/dev up -d` starts both together (see the parent README). Everything below also works standalone in this repo.

Quality gates run locally with `make check` (see below); CI builds the release image and runs the expand/contract check.

## What exists today

The whole API in `api/openapi.yaml` is implemented (spec `1.0.0`, generated):

| Area | State |
|---|---|
| Health | `GET /healthz` liveness (`200 ok`), `GET /readyz` readiness (`200 ready` only if the DB answers and `DATA_DIR` is writable, else a terse `503`, no paths or SQL) |
| Data | Settings, tree, folders, lists, items (six types), Markdown content with versions, one image per item, search, recent items, trash with restore and purge |
| Cross-cutting | Request IDs, access log, panic recovery, security headers, Huma request validation (`If-Match`, body limits, unknown fields), problem+json errors |
| Operations | Config validated at startup, `log/slog`, migrations, backup/restore/verify scripts |
| Log viewer | `LOGS_UI` (on unless `APP_ENV=production`): `GET /admin/logs/view` (HTML) and `GET /admin/logs` (JSON) over the newest 2000 log records in memory; hidden from the spec, no auth, keep it on the private address (see [docs/modules/logs.md](docs/modules/logs.md)) |

There is no authentication, so the API is published on `127.0.0.1` only. See the parent README, "Security boundary".

## Layout

```text
cmd/api/main.go        wiring only: config → logger → pgx pool → services → chi router → http.Server (graceful shutdown)
internal/platform      shared infra: config, database (sqlc/ GENERATED, never edit), clock, etag, httpserver (Huma on chi), problem, scope, logs, testdb, archtest
internal/apperr        typed domain errors (no HTTP)
internal/folders       folders: create, get, rename, move, reorder (Huma)
internal/lists         lists: create, get with path, update, move, reorder (Huma)
internal/definitions   GET /api/v1/types, the built-in types served from the Go registry (items/types/registry.go)
internal/tree          GET /api/v1/tree, the flat sidebar read (handler only)
internal/items         items; items/types validates per-type metadata
internal/content       Markdown content and versions
internal/images        upload validation, thumbnails, serving, orphan sweeper
internal/storage       FileStore (local files)
internal/trash         delete, list, restore, purge
internal/search        search and recent items
internal/workspace     bootstrap of the single workspace; settings (Huma)
internal/httpapi       HTTP composition root: Huma router, probes, Spec() for cmd/specgen (the spec is generated from it)
cmd/specgen            prints the OpenAPI document for `make spec`
internal/lookup        metadata lookup (TMDB, Open Library, Hardcover gap-fill): merge rules, provenance, weekly TMDB refresh (Huma)
internal/stores        fixed online store provider keys (Huma)
internal/integrations  bounded upstream clients: tmdb, openlibrary, hardcover
api/openapi.yaml       the canonical API contract (spec wins over design-docs/API.md)
db/migrations          golang-migrate files, embedded in the binary by `db/migrations.go`: one baseline `000001_init` (up/down); later changes append as new numbered files
db/queries             sqlc queries (generated into internal/platform/database/sqlc)
scripts/               backup, restore, verify, compose exposure check, reset-dev
docs/                  one short doc per module
```

The binary also has a `healthcheck` subcommand (`/app/api healthcheck`) used by the compose healthcheck, because the
distroless runtime image has no shell or curl.

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.27.3 |
| Docker with Compose v2 | any recent (Docker Desktop 28 tested) |
| Node | 24+ (for `make lint-api`, which runs Redocly through `npx`; `make check` includes it, so it is needed for the gate) |
| Git, `gh` | for PRs |
| `jq`, optionally `zstd` | for the backup scripts |

Postgres 18.6 runs as a pinned Docker image (golang-migrate is a Go library inside the API binary; its 4.20.1 image is used only by `make migration`), so there's nothing else to install.

## Start

```bash
cp .env.example .env   # set POSTGRES_PASSWORD (URL-safe characters only)
docker compose up --build
```

This starts `db`, runs `migrate` once (the API image's `/app/api migrate`: applies the embedded migrations to `DATABASE_URL` and exits 0), then starts `api` on `http://127.0.0.1:8080` (loopback only, never exposed to the LAN).

| Task | Command |
|---|---|
| Start in the background | `docker compose up --build -d` |
| Stop (keep data) | `docker compose down` |
| Logs | `docker compose logs -f api` |
| Health | `curl -fsS http://127.0.0.1:8080/healthz` (liveness), `curl -fsS http://127.0.0.1:8080/readyz` (DB + data dir) |
| Migrate | `make migrate` |
| New migration | `make migration name=<snake_case>` |
| Tests / lint / format check / format | `make test` / `make lint` / `make fmt-check` / `make fmt` |
| Regenerate the API spec | `make spec` (writes `api/openapi.yaml` from the Huma operations); `make check-spec` fails if it is stale |
| Lint the API spec | `make lint-api` (Redocly, config in `redocly.yaml`) |
| Regenerate code | `make generate` (sqlc) |
| All gates | `make check` (needs Docker running) |
| Vulnerability scan | `make audit` |
| Dead code report | `make deadcode` (not part of `make check`) |
| Safe reset (deletes DB + uploads) | `make reset-dev` (asks first, refuses when `APP_ENV=production`) |
| Backup / restore / verify | `scripts/backup.sh`, `scripts/restore.sh`, `scripts/verify-restore.sh` (see [Backup and restore](#backup-and-restore)) |
| Backup round-trip test (AT-17) | `make test-backup` (about a minute, not part of `make check`) |
| API smoke test | `make smoke` (curl + jq against a running stack, `API_URL` defaults to `http://localhost:8090`, not part of `make check`) |

`make check` = gofmt check, golangci-lint (including goimports and the depguard layer rules), the Redocly spec lint, shellcheck on `scripts/` (pinned Docker image), generated-code and API-spec drift checks, `go test ./...` (testcontainers Postgres, including the architecture test), a check that compose publishes ports on `127.0.0.1` only, and a Docker image build.

Run `make smoke` after a deploy, a compose change or a migration change, and before a release. It walks one end-to-end flow over HTTP (folder, list, item, image, sort, search, trash, restore, purge) and removes what it created. Point it at a fresh dev stack, never at `./deploy/test` (the user's manual environment): `./deploy/dev up -d --fresh`, then `make smoke` (or `API_URL=http://host:port make smoke`).

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
- **Back up before every migration in production** (upgrade steps: backup, pull new images, run the image's `/app/api migrate`, restart; the server never migrates at startup, check `/readyz`). To roll back, restore the backup taken before the upgrade: a migration `down` does not bring the files back and can lose data.
- **Retention:** keep at least 7 daily and 4 weekly copies **off the device** (another disk, NAS or cloud of your choice). Backups hold all your data and the images unencrypted; keep them private and never commit them (`backups/` is gitignored).
- Only restore backups you made yourself: the manifest checksums detect corruption, not a malicious archive.

## Host-run loop (faster iteration)

```bash
cp compose.override.example.yaml compose.override.yaml   # publishes 127.0.0.1:5432
docker compose up -d db
make migrate
make run                                                 # uses .env, DATA_DIR=./data
```

`make run` reads `.env` and builds `DATABASE_URL` for `localhost:5432` from `POSTGRES_PASSWORD` (set `DATABASE_URL` in `.env` to override).

## API contract

`api/openapi.yaml` is generated from the Huma operations and is the published contract for every endpoint, schema, header and
error. To change it, change the handler types or registration, run `make spec`, and commit both. `make check` fails if the
committed spec is stale (`check-spec`) and lints it with Redocly. Constraints the spec does not carry are listed in
`docs/api-notes.md`. Contract changes follow `design-docs/AGENT_WORKFLOW.md` §6.

## Releases

Every version tag `vX.Y.Z` runs `.github/workflows/release-image.yml`: it builds the API image for linux/arm64 only
(on the native ubuntu-24.04-arm runner, no QEMU: prod and dev are arm64) and pushes it to GHCR as the private package `ghcr.io/0xfederama/collections-app-backend:X.Y.Z`. A tag is
not a deploy. Publishing a GitHub Release (marked latest) for that tag is the prod approval; the Pi then deploys it
(D-117). Orchestrators may leave a draft release, never publish it.

Rollback never restores a backup (D-118): it runs the previous release on the newer schema, so migrations must be
expand/contract (see CLAUDE.md). `.github/workflows/expand-contract.yml` enforces it on every PR, `main` and tag: it applies
this commit's migrations, starts the latest GitHub Release's image from GHCR on that schema and runs `make smoke`.

## Configuration

Environment variables are validated at startup. See `.env.example` and `design-docs/BACKUP_AND_DEPLOYMENT.md` §3. TMDB lookup is optional: request a key in [TMDB account API settings](https://www.themoviedb.org/settings/api) and set `TMDB_API_KEY` in the parent repo's `deploy/server/.env`; keep it server-side. A missing key disables lookup, while item creation keeps the typed title. The app must show an [approved TMDB logo and attribution](https://www.themoviedb.org/about/logos-attribution) in About; [TMDB watch-provider results](https://developer.themoviedb.org/reference/tv-series-watch-providers) require JustWatch attribution. Movie and TV metadata imported from TMDB is refreshed weekly and never expires; user images are kept. Backups containing TMDB data must also be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use); production backup retention is shorter.

## Troubleshooting

- **Tool versions:** `go version` must be 1.27+, `docker compose version` must be v2.
- **Port already in use:** set `API_PORT` in `.env`.
- **`db` exits with "superuser password is not specified":** `POSTGRES_PASSWORD` is empty. Copy `.env.example` to `.env` and set it.
- **DB connection errors:** `docker compose ps` should show `db` healthy. For the host loop, make sure `compose.override.yaml` exists and `DATABASE_URL` (if you set it) uses `localhost`.
- **Migrations not applied:** `make migrate`, then `docker compose logs migrate`. `docker compose up --build` applies 000001 on a fresh database.
- **Storage permissions:** `DATA_DIR` must exist and be writable (the container uses UID 65532 and the `appdata` volume). `/readyz` returns 503 if it isn't.
- **`make check` fails on tests:** Docker must be running. The first lint run compiles golangci-lint and is slow once.
- **Stale generated code:** run `make generate` (sqlc) or `make spec` (API spec) and commit the result. `make check` fails on drift.
