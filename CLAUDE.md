# CLAUDE.md — collections-app-backend

Go REST API + PostgreSQL for the Collection App. It owns the canonical API contract `api/openapi.yaml`.

This repo is the `backend/` submodule of the parent repo `0xfederama/collections-app` (sibling: `frontend-web/`). Branch and PR here; the orchestrator bumps the pointer in the parent after merge (D-043).

## Design docs (canonical, read before working)

`/Users/federico/Developer/collections-app/design-docs/`. Always use this **absolute** path (worktrees break `../`).
- Every session: `AGENT_WORKFLOW.md`, then `STATUS.md` and `DECISIONS.md` (open questions + recent entries).
- These are **living** docs, versioned in the **parent** repo (D-043). Update `STATUS.md` when you start or finish a task or open a PR, and record decisions as they happen (AGENT_WORKFLOW §7). Re-read before editing, and make small targeted edits. Commit design-doc edits in the parent, never in this repo (AGENT_WORKFLOW §7, D-044).
- Backend work: `DATA_MODEL.md`, `API.md`, `ARCHITECTURE.md` §2/§4/§5, and the task in `IMPLEMENTATION_PLAN.md`.
- If the code must deviate from the docs, follow AGENT_WORKFLOW §7. Never deviate silently.

## Session rules (summary of AGENT_WORKFLOW)

- **Never push to `main`.** Branch `<feature|fix|hotfix|refactor|test|docs|chore|ci>/<desc>` from `origin/main`, use Conventional Commits, push the branch, and open a PR with `gh pr create`. Don't merge unless the user says so.
- **Dispatch cheaper subagents** (`.claude/agents/`):
  - `researcher` (sonnet): versions, library docs, "how does X work here". Must cite sources.
  - `test-writer` (sonnet): writes tests. **You review and run them yourself.**
  - `docs-keeper` (haiku): updates `docs/` before every PR.
  - Built-in `Explore` handles broad code search.
  - Keep schema, concurrency, trash, storage, and security logic in the main session.
- Update **this file** in the same PR when commands, layout, conventions, or gotchas change. Keep it under ~200 lines.
- MVP scope fence: no auth, TMDB, AI, public-API features, WebSockets, ORM, Redis, or queues (ORCHESTRATOR_BRIEF §2).

## Commands
No CI (D-031, D-032): the gates run locally. Run `make check` before every PR and paste the output in the PR.
| Purpose | Command |
|---|---|
| All checks (gofmt, golangci-lint incl. goimports, tests, compose exposure, Docker build) | `make check` |
| Start everything (db + migrate + api on 127.0.0.1:8080) | `docker compose up --build` (add `-d` to detach) |
| Stop / logs / health | `docker compose down` / `docker compose logs -f api` / `curl -fsS http://127.0.0.1:8080/readyz` |
| Tests (need Docker for testcontainers) | `make test` |
| Lint / format check / format | `make lint` / `make fmt-check` / `make fmt` |
| Apply migrations | `make migrate` |
| New migration | `make migration name=<snake_case>` |
| Run API on the host (needs `compose.override.yaml` + `docker compose up -d db`) | `make run` |
| Regenerate code | `make generate` (no-op until B-02/B-03) |
| Vulnerability scan (network) | `make audit` |
| Delete dev containers, volumes and `./data` | `make reset-dev` |

Versions and tool choices: DECISIONS.md "Toolchain versions (R-1)" and D-035–D-041. Go tools are pinned via the `go.mod` `tool` directive (`go tool govulncheck`), golangci-lint via `go run …@v2.14.0` in the Makefile.

## Layout & conventions
- `cmd/api`: wiring only. `internal/<domain>`: services and domain errors. `internal/httpapi`: handlers, middleware, problem mapping. `internal/database/sqlc`: generated code, **never edit**.
- SQL lives in `db/queries/*.sql` (sqlc). Every query filters by `workspace_id`. Every UPDATE sets `updated_at` and, for editable resources, `version = version + 1`.
- Multi-statement writes go through `database.InTx`.
- Files: write before the tx, delete after commit (ARCHITECTURE §4).
- Errors: return typed domain errors, and map them to problem+json only in `httpapi`. Never leak SQL, paths, or stack traces.
- No CORS middleware (D-033, same-origin everywhere). Runtime image is distroless with no shell, so the container healthcheck is `/app/api healthcheck`.
- Tests: table-driven, with names prefixed by the acceptance-test ID when applicable (`TestAT10_…`). Integration tests use testcontainers Postgres and a `t.TempDir()` data dir.

## Gotchas
<!-- Add entries as discovered: one line each, with the date. -->
- 2026-10-13: `migrate up` exits 1 on a migrations dir with no migrations (D-037). The compose `migrate` service skips when there is no `*.up.sql`.
- 2026-10-13: `make check` needs Docker running (testcontainers) and compiles golangci-lint on first run (slow once, then cached).
- 2026-10-13: `make check-compose` fails if any published port is not on `127.0.0.1` (AT-18). It runs against `docker compose config`, so a local `compose.override.yaml` is included.
- 2026-10-14: The Dockerfile copies only `cmd/` and `internal/`. A new top-level Go dir (e.g. generated code outside `internal/`) must be added there; `make check-docker` catches it. Keep the `golang:` tag in step with `go.mod`.
- 2026-10-14: `noctx` is enabled: outbound HTTP calls must carry a context (`http.NewRequestWithContext`).
- 2026-10-13: Postgres 18 image keeps data under `/var/lib/postgresql` (the volume mounts there, not `/var/lib/postgresql/data`).
- 2026-10-13: The `POSTGRES_PASSWORD` is embedded in `DATABASE_URL` (compose and `make run`), so use URL-safe characters only. Never write `user:pass@host` URLs in tracked files (GitGuardian flags them, even for placeholders or variables): pass credentials as query parameters (`?user=…&password=…`) and leave secret values blank in examples.

## Working guidelines

### Shell
- Check the parent directory exists before creating files or directories. Quote paths with spaces (`"path with spaces/file"`).
- Prefer absolute paths over `cd` (e.g. `go test /abs/path/...`). Chain commands with `&&` or `;`, not newlines.
- Use the Read/Grep/Glob tools instead of `cat`/`grep`/`find`/`ls`. If shell search is unavoidable, use `rg`.
- Give every command a clear 5–10 word description. Use `run_in_background` for long-running processes, never `sleep`.

### Proactiveness
- Do what was asked, including natural follow-ups (tests, docs, CLAUDE.md), but don't surprise the user with unrequested actions. If asked *how*, answer first.
- **Never guess.** Don't guess that a method exists, an object's type, that tests pass, or business-logic flows. Verify in code or docs, or ask.

### Conventions
- Mimic the surrounding code style, reuse existing helpers, and read imports before editing.
- Never assume a library is available. Check `go.mod` first, and justify any new dependency in the PR.
- Security: never log or commit secrets or keys. `.env` is gitignored, and `.env.example` holds placeholders only.
