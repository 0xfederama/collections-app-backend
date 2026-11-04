# CLAUDE.md — collections-app-backend

Go REST API + PostgreSQL for the Collection App. The API contract `api/openapi.yaml` is **generated** from the handlers.
`backend/` is a submodule of `0xfederama/collections-app` (sibling: `frontend-web/`). Branch and PR here; the orchestrator bumps the pointer in the parent after merge (D-043). `AGENTS.md` only points here.

## Design docs (read before working)
`/Users/federico/Developer/collections-app/design-docs/` (absolute path; worktrees break `../`): `AGENT_WORKFLOW.md`, then `STATUS.md` and `DECISIONS.md`. Living docs, versioned in the **parent** repo: commit design-doc edits there, never here (AGENT_WORKFLOW §7). Backend work: `DATA_MODEL.md`, `ARCHITECTURE.md`, the feature brief in `features/`. Deviating from the docs: AGENT_WORKFLOW §7, never silently.

## Session rules (summary)
- **Never push to `main`.** Branch `<feature|fix|hotfix|refactor|test|docs|chore|ci>/<desc>` from `origin/main`, Conventional Commits, PR with `gh pr create`; the repo orchestrator squash-merges (`gh pr merge --squash --delete-branch`), then deletes branch and worktree.
- Workflow v2: the orchestrator splits stories into tasks and dispatches `worker` agents (`.claude/agents/`); `docs-keeper` updates `docs/` before every PR; review only FLAGGED items; run the full gate once per PR (AGENT_WORKFLOW §1b "Test runs"). Schema, concurrency, trash, storage and security work is `[CRUCIAL]`.
- Update this file in the same PR when commands, layout, conventions or gotchas change. Keep it under ~200 lines.
- Scope fence: no auth, AI, public-API features, WebSockets, ORM, Redis or queues.

## Commands (gates run locally; CI builds the release image on a `vX.Y.Z` tag and runs the expand/contract check, see README "Releases")
| Purpose | Command |
|---|---|
| All checks (gofmt, golangci-lint with layer rules, spec drift + Redocly lint, sqlc drift, tests incl. migrations and architecture test, compose exposure, shellcheck, Docker build) | `make check` |
| Tests (need Docker: testcontainers) | `make test` |
| Regenerate sqlc / the API contract | `make generate` / `make spec` (commit both) |
| New migration | `make migration name=<snake_case>` |
| Start everything on 127.0.0.1:8080 / stop / logs | `docker compose up --build` (needs `.env` with `POSTGRES_PASSWORD`) / `docker compose down` / `docker compose logs -f api` |
| Backup round trip (not in `check`) | `make test-backup` |
| API smoke over HTTP (against `./deploy/dev up -d --fresh` or a compose stack; `API_URL`) | `make smoke` |
| Vulnerabilities / dead code | `make audit` / `make deadcode` |
| Reset dev containers, volumes, `./data` | `make reset-dev` |

## Architecture (docs/architecture.md has the diagram)
Package by feature, one direction: **handler → service → repository → database**.
- `cmd/api`: wiring only. `internal/httpapi`: the HTTP composition root (`NewRouter`, `API`, adapters between features, health probes, `spec.go`); `cmd/specgen` prints the spec.
- Features (`folders lists tree definitions items content images trash search lookup workspace stores logs`): `model.go`, `repository.go` (the ONLY file that imports sqlc/pgx; one plain query per method; workspace from `scope.WorkspaceID(ctx)`), `service.go` (rules, transactions; declares its `Store`/`Repo` interfaces; no sqlc, no net/http, no huma), `handler.go` + `wire.go` (Huma operations through `httpserver.Op`, `…JSON` wire types). A service exists only where there are real rules. `definitions` is handler + wire only: the five built-in types are Go code in `internal/items/types/registry.go` (no custom fields, no type tables; F-023).
- `internal/platform/{database,etag,clock,config,testdb,problem,scope,httpserver,logs,archtest}`: shared plumbing. `internal/apperr`: typed domain errors (no HTTP); `platform/problem` is the only mapper to RFC 9457 problem+json and owns the table of error codes.
- `internal/integrations/{tmdb,openlibrary,hardcover}`: provider clients (no DB). `internal/storage`: `FileStore` on `os.Root`.
- **Layer rules are enforced** by golangci `depguard` (per file) and `platform/archtest/layers_test.go`: do not weaken them. A feature never imports another feature's handler/repository; cross-feature needs go through a small interface declared by the consumer and wired in `httpapi/router.go`.
- Migrations: `db/migrations` (baseline `000001_init`, then append), embedded by `db/migrations.go` and applied only by `/app/api migrate` (compose `migrate` service; the server never migrates). No triggers, no PL/pgSQL, no `ON DELETE CASCADE`: deletes are explicit and children first.
- **Migrations are expand/contract (D-118, `[CRUCIAL]`)**: the previous release's API must keep working on the new schema, because rollback means running the old version on the newer schema (no down migrations, no restore). Allowed: new tables, nullable or defaulted columns, new indexes, new constraints the old code cannot violate. Never in the same release: drop, rename, change a type, add NOT NULL without a default, or tighten a constraint the old code may break; ship the removal in a later release, once no live version uses the old shape. Each file runs in one transaction: no `BEGIN`/`COMMIT`, `CONCURRENTLY`, `VACUUM` (test-enforced). CI (`.github/workflows/expand-contract.yml`) applies the new migrations, starts the latest GitHub Release's image on them and runs `make smoke`. Queries: `db/queries/<feature>.sql`, generated code in `internal/platform/database/sqlc` (never edit).

## Conventions
- Go 1.27, `log/slog`, errors wrapped with `%w`, context everywhere, time from the injected `Clock`, no package-level state (the one exception: Huma's error constructors, set once in `problem.Install()`).
- Multi-statement writes in one transaction (`InTx`); files are written BEFORE the tx and deleted AFTER commit; structural writes take the tree lock first (`LockTree`).
- **Never call another feature's reader inside `InTx`** (a second pool connection can stall the pool): read first, remember the error, return it at the old point in the rules.
- Return typed `apperr` errors from services; never build problems by hand in handlers except `problem.PreconditionRequired()`; never leak SQL, paths or stack traces. Handlers register through `httpserver.Op`; If-Match is parsed in the handler (`ifMatchFirst` middleware where 428 must precede the body).
- Tests: table-driven, names prefixed by the acceptance-test ID when there is one; integration tests on testcontainers Postgres via `platform/testdb`; services tested with fakes; services take the workspace from the context, so tests call them with `scope.With(ctx, ws)`.
- Files stay under ~300 lines unless there is a reason; comments say why. No new dependency without a line in the PR.
- Security: never log or commit secrets; `.env` is gitignored, `.env.example` has placeholders. Never write `user:pass@host` URLs in tracked files (GitGuardian): pass credentials as query parameters.

## Changing the API contract
Edit the Go registration (operation id, summary, tags, `Errors`, wire struct), run `make spec`, commit code and `api/openapi.yaml`. Keep operation ids and schema names stable. Huma validates every struct tag before the handler: adding a tag can change error answers (see `docs/api-notes.md` for constraints enforced by services only). Add a new error code to `platform/problem/codes.go` (test-enforced). Tag `api-vX.Y.Z` marks contract milestones; the web pins to tags.

## Gotchas
- `make check` needs Docker (testcontainers) and compiles golangci-lint on first run. The Dockerfile copies `cmd/` and `internal/` only; a new top-level Go dir must be added (`make check-docker`). `make check-compose` fails if a published port is not on `127.0.0.1`.
- `docker compose up` needs a `.env` with `POSTGRES_PASSWORD` (URL-safe characters only: it is embedded in `DATABASE_URL`). Postgres 18 keeps data under `/var/lib/postgresql`.
- sqlc: comments go after the `-- name:` line; a recursive CTE filter needs `sqlc.arg(x)::uuid` (there are none left); `check-generate` compares to HEAD, commit generated code first; `noctx` is enabled (outbound HTTP needs a context).
- Item lists are filtered/sorted/paged in Go (`items/listquery.go`) over one plain query; text order is bytewise on lower-cased strings (the postgres alpine image uses byte collation).
- Item/list writes: a PATCH that changes nothing answers 200 with the same version; `Optional[T]` tells absent from `null`. Responses must set every nullable field explicitly.
- Images: `images.Strip` removes EXIF/GPS/XMP on upload. The sweeper and auto-purge need a workspace-scoped context (`scope.With`): an unscoped sweeper refuses to run.
- Trash: delete marks folders, then their lists, then those lists' items; restore clears the root first and deletes the batch row last (D-051); purge deletes children before parents and files after commit.
- Lookup: movie/TV items are refreshed from TMDB weekly, overwriting fetched fields and titles (D-123, no expiry), also on demand (`mode: refresh`, `POST /lists/{id}/lookup-refresh`: 50 items per call, oldest first); books are never refreshed (D-095); Open Library is primary for books, Hardcover only fills gaps (optional `HARDCOVER_API_KEY`); `TMDB_API_KEY` optional; tests use fixtures, never the network; `TMDB_MOCK_FIXTURE` enables `[mock] movie name` / `[mock] book name`.
- Streaming: `streaming.link` is TMDB's own JustWatch page (imported, never client-written); store and platform links are built by the web.
- Logs: `LOGS_UI` (default on unless `APP_ENV=production`) serves `/admin/logs` and `/admin/logs/view` with no auth on the API listener; not in the spec.
- Backup scripts (`scripts/lib/backup-common.sh`): never write `-v appdata:/data` for a one-off container; containers that must not eat stdin need `</dev/null`; wait for Postgres over TCP.

## Working guidelines
- Shell: quote paths with spaces, prefer absolute paths, use the Read/Grep/Glob tools (or `rg`, never `rg -h`), chain with `&&`.
- Never guess: verify a method, type or flow in the code or the docs. Do what was asked, including natural follow-ups (tests, docs, this file).
