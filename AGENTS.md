# AGENTS.md — collections-app-backend

Go REST API + PostgreSQL for the Collection App. It owns the canonical API contract `api/openapi.yaml`.

This repo is the `backend/` submodule of the parent repo `0xfederama/collections-app` (sibling: `frontend-web/`). Branch and PR here; the orchestrator bumps the pointer in the parent after merge (D-043).

## Design docs (canonical, read before working)

`/Users/federico/Developer/collections-app/design-docs/`. Always use this **absolute** path (worktrees break `../`).
- Every session: `AGENT_WORKFLOW.md`, then `STATUS.md` and `DECISIONS.md` (open questions + recent entries).
- These are **living** docs, versioned in the **parent** repo (D-043). Update `STATUS.md` when you start or finish a task or open a PR, and record decisions as they happen (AGENT_WORKFLOW §7). Re-read before editing, and make small targeted edits. Commit design-doc edits in the parent, never in this repo (AGENT_WORKFLOW §7, D-044).
- Backend work: `DATA_MODEL.md`, `API.md`, `ARCHITECTURE.md` §2/§4/§5, and the task in `IMPLEMENTATION_PLAN.md`.
- If the code must deviate from the docs, follow AGENT_WORKFLOW §7. Never deviate silently.

## Session rules (summary of AGENT_WORKFLOW)

- **Never push to `main`.** Branch `<feature|fix|hotfix|refactor|test|docs|chore|ci>/<desc>` from `origin/main`, use Conventional Commits, push the branch, and open a PR with `gh pr create`. In workflow v2 the repo orchestrator merges its own PRs once the gate is green (D-077): squash merge only (`gh pr merge --squash --delete-branch`), never merge commits; a non-trivial rebase and its merge go to a Sonnet 5.5 subagent.
- **Workflow v2 (AGENT_WORKFLOW §1b, D-074).** This session is the **repo orchestrator** (Opus 5.5, medium effort): it splits
  the repo's stories from `design-docs/features/F-NNN-*.md` into tasks and dispatches agents from `.Codex/agents/`:
  - `worker` (simple tier by default: Haiku 5.5 / GPT 6 Luna, medium; the orchestrator dispatches very complex tasks on the complex tier: Sonnet 5.5 / GPT 5.6 Terra, medium; AGENT_WORKFLOW §2): implements one task with its tests, runs the tests that matter, commits, and reports
    **FLAGS**. Review **only flagged items**, and run the full gate yourself once before each PR.
  - Run tests wisely (§1b "Test runs", D-076): targeted tests while working, the full gate once per PR, slow suites
    (e2e, smoke, backup round trip) only when the change touches what they cover or before a release.
  - `researcher` (complex tier): versions, library docs, "how does X work here". Must cite sources.
  - `docs-keeper` (simple tier): updates `docs/` before every PR.
  - Built-in `Explore` handles broad code search.
  - Keep schema, concurrency, trash, storage, and security logic as `[CRUCIAL]` flags you always review.
- Update **this file** in the same PR when commands, layout, conventions, or gotchas change. Keep it under ~200 lines.
- MVP scope fence: no auth, TMDB, AI, public-API features, WebSockets, ORM, Redis, or queues (ORCHESTRATOR_BRIEF §2).

## Commands
No CI (D-031, D-032): the gates run locally. Run `make check` before every PR and paste the output in the PR.
| Purpose | Command |
|---|---|
| All checks (gofmt, golangci-lint incl. goimports, Redocly spec lint, sqlc + oapi-codegen drift, tests incl. migrations up/down/up, compose exposure, Docker build) | `make check` |
| Start everything (db + migrate + api on 127.0.0.1:8080) | `docker compose up --build` (add `-d` to detach) |
| Stop / logs / health | `docker compose down` / `docker compose logs -f api` / `curl -fsS http://127.0.0.1:8080/readyz` |
| Tests (need Docker for testcontainers) | `make test` |
| Lint / format check / format | `make lint` / `make fmt-check` / `make fmt` |
| Apply migrations | `make migrate` |
| New migration | `make migration name=<snake_case>` |
| Run API on the host (needs `compose.override.yaml` + `docker compose up -d db`) | `make run` |
| Regenerate code | `make generate` (sqlc, then oapi-codegen) |
| Lint the API spec (needs Node >= 24, runs Redocly via `npx`) | `make lint-api` |
| Vulnerability scan (network) | `make audit` |
| Delete dev containers, volumes and `./data` | `make reset-dev` |
| Backup / restore / verify (Postgres + images, D-066) | `scripts/backup.sh [dest]`, `scripts/restore.sh <dir> [--force]`, `scripts/verify-restore.sh <dir>` |
| AT-17 backup round trip (isolated compose project, ~1 min, not in `make check`) | `make test-backup` |
| API smoke over HTTP (curl+jq; `API_URL` default `http://localhost:8090`; run against `./deploy/test up -d --fresh`, never dev data; not in `make check`) | `make smoke` |
| shellcheck of `scripts/` (pinned Docker image, part of `make check`) | `make lint-scripts` |

Versions and tool choices: DECISIONS.md "Toolchain versions (R-1)" and D-035–D-041. Go tools are pinned via the `go.mod` `tool` directive (`go tool govulncheck`), golangci-lint via `go run …@v2.14.0` in the Makefile.

## Layout & conventions
- `cmd/api`: wiring only. `internal/<domain>`: services. `internal/apperr`: typed domain errors (no HTTP). `internal/tree` (folders, lists), `internal/trash` (delete, trash list, restore, purge), `internal/items` (items; `items/types` = per-type metadata validators), `internal/search` (GET /search, GET /recent-items), `internal/images` (upload validation, thumbnails, image writes, serving, orphan sweeper), `internal/storage` (`FileStore`, local impl on `os.Root`), `internal/clock`: injected `Clock`. `internal/httpapi`: handlers, middleware (request id, security headers, access log, recovery, request guard), problem mapping, `etag`. `internal/database/sqlc`: generated code, **never edit**; regenerate with `make generate` (config `sqlc.yaml`, schema = `db/migrations`, queries = `db/queries`).
- **Contract:** `api/openapi.yaml` (OpenAPI 3.0.3) is canonical and wins over API.md. `internal/httpapi/oapi/oapi.gen.go` is generated by oapi-codegen (`oapi-codegen.yaml`: models + chi server + strict server), **never edit**. Change the spec first, `make generate`, commit both. Spec changes follow AGENT_WORKFLOW §6 and at most one PR may have the spec open. Keep `operationId`s stable (B-04 onward implements `StrictServerInterface`). `redocly.yaml` turns invalid examples into errors; the examples feed the web MSW mocks.
- SQL lives in `db/queries/*.sql` (sqlc). Every query filters by `workspace_id`. Every UPDATE sets `updated_at` and, for editable resources, `version = version + 1`.
- Multi-statement writes go through `database.InTx`.
- Files: write before the tx, delete after commit (ARCHITECTURE §4).
- Errors: return typed domain errors, and map them to problem+json only in `httpapi`. Never leak SQL, paths, or stack traces.
- No CORS middleware (D-033, same-origin everywhere). Runtime image is distroless with no shell, so the container healthcheck is `/app/api healthcheck`.
- Tests: table-driven, with names prefixed by the acceptance-test ID when applicable (`TestAT10_…`). Integration tests use testcontainers Postgres and a `t.TempDir()` data dir.

## Gotchas
- 2026-10-29: F-019-B2: the baseline `000001_init` was amended in place (no `000002`): no triggers, no PL/pgSQL, no `ON DELETE CASCADE` (FKs are NO ACTION, pinned by `TestSchema_NoCascades`). Built-in types are seeded from Go (`internal/seed`, `workspace.Bootstrap` on every start, idempotent, never overwrites rows); tests that insert a workspace by SQL call `seed.BuiltinTypes(ctx, sqlc.New(pool), ws)`. Hard deletes are explicit and children-first: trash purge (`purgeBatch` collects ids in Go), type delete (fields first), content pruning, lookup cleanup, replaced attachments. Any new delete must remove its children itself. A dev DB created before this change must be reset.
- 2026-10-20: F-019-B1: `db/migrations` is one baseline, `000001_init` (the schema of the former migrations 000001..000025, proven equal by `pg_dump`); later changes append new migrations. Gotchas below that cite "migration 0000NN" or old versions are history: the old chain is in git before this change. `strip-images` and the `streaming.link` backfill are gone (uploads still strip metadata via `images.Strip`).
<!-- Add entries as discovered: one line each, with the date. -->
- 2026-10-14: sqlc fails on an empty `db/queries` ("no queries"), so `db/queries/schema.sql` keeps one smoke query. sqlc ignores `*.down.sql`. Nullable `timestamptz` needs its own override (`*time.Time`), `emit_pointers_for_null_types` does not cover it under pgx/v5. `check-generate` compares the working tree to HEAD, so commit generated code before running it.
- 2026-10-14: Constraint and index names are explicit (`<table>_<cols>_{pkey,key,fkey,check,idx}`), so services can map `pgconn.PgError.ConstraintName` to domain errors. `trash_batch_id` FKs have no ON DELETE action (D-051).
- 2026-10-14: `internal/database` tests start their own Postgres and create one database per test; migration files are read from `../../db/migrations`.
- 2026-10-13: `migrate up` exits 1 on a migrations dir with no migrations (D-037). The compose `migrate` service skips when there is no `*.up.sql`.
- 2026-10-13: `make check` needs Docker running (testcontainers) and compiles golangci-lint on first run (slow once, then cached).
- 2026-10-13: `make check-compose` fails if any published port is not on `127.0.0.1` (AT-18). It runs against `docker compose config`, so a local `compose.override.yaml` is included.
- 2026-10-14: The Dockerfile copies only `cmd/` and `internal/`. A new top-level Go dir (e.g. generated code outside `internal/`) must be added there; `make check-docker` catches it. Keep the `golang:` tag in step with `go.mod`.
- 2026-10-14: `noctx` is enabled: outbound HTTP calls must carry a context (`http.NewRequestWithContext`).
- 2026-10-13: Postgres 18 image keeps data under `/var/lib/postgresql` (the volume mounts there, not `/var/lib/postgresql/data`).
- 2026-10-13: The `POSTGRES_PASSWORD` is embedded in `DATABASE_URL` (compose and `make run`), so use URL-safe characters only. Never write `user:pass@host` URLs in tracked files (GitGuardian flags them, even for placeholders or variables): pass credentials as query parameters (`?user=…&password=…`) and leave secret values blank in examples.

- 2026-10-14: oapi-codegen runs with `nullable-type: true`, so nullable fields are `nullable.Nullable[T]` (PATCH can tell absent from `null`). An **unset** `Nullable` in a response struct marshals as the zero value (`""`, `{}`), not `null`: always set it (`nullable.NewNullNullable[T]()` or `NewNullableWithValue`).
- 2026-10-14: The generated strict server alone decodes bodies with a plain `json.NewDecoder(...).Decode` (no unknown-field or size check, `text/plain` errors). B-04 fixes that centrally in `internal/httpapi/guard.go` and `problem.go` (D-055–D-057): handlers never check these. `If-Match` is declared `required: false` on purpose (a `required: true` header makes the generated wrapper answer 400 first); the guard answers 428 and stores the parsed version for `httpapi.IfMatchVersion(ctx)`.
- 2026-10-14: The guard finds the operation through `chi.RouteContext(...).RoutePattern()` and the spec embedded in `oapi.gen.go` (`embedded-spec: true`), so it only runs for routes the spec declares and a new route needs no registration. It runs after path binding, so a bad UUID in the path answers 400 before 428. The access-log middleware installs its own chi route context to read the matched pattern afterwards; do not replace it.
- 2026-10-14: `ifMatchPrefix` in `guard.go` (operationID → ETag prefix) must be extended by hand for any new operation that requires `If-Match`; AT-10 (`api_test.go`) lists them independently from API.md §3.
- 2026-10-14: In OpenAPI 3.0.3 `nullable: true` next to `allOf: [$ref]` makes Redocly reject `null` examples, so `Price`, `ImageSummary` and the `Image` union carry `nullable: true` themselves. Descriptions with `: ` or a leading backtick must be quoted in the YAML.
- 2026-10-14: Tests needing Postgres use `internal/testdb` (`AdminURL`, `NewMigrated`, `Terminate` from `TestMain`): one shared container per test binary, one migrated database per test. `httpapi` tests reuse their own `sharedPostgres(t)` URL with `testdb.NewMigrated`.
- 2026-10-14: `workspace.Bootstrap` runs in `cmd/api` before the router and its ID goes into `Deps.WorkspaceID`. Services take the workspace ID as an argument. A PATCH that changes nothing answers 200 with the current version and no bump. `docker compose up` needs a local `.env` with `POSTGRES_PASSWORD` set, or the db container is unhealthy.
- 2026-10-14: sqlc (v1.31) rejects a recursive CTE whose outer `SELECT` filters on a plain `@param` ("column reference is ambiguous"): write `sqlc.arg(x)::uuid` there. Put explanatory comments after the `-- name:` line, not before it.
- 2026-10-14: Structural writes on folders/lists (create, move, reorder) call `lockTree` first (one advisory lock per workspace, D-060); the cycle check relies on it. Trash delete/restore (B-10) must take it too.
- 2026-10-14: Item metadata is free-form in the spec, so the guard cannot see unknown keys: `items/kinds.Validate` returns `apperr.UnknownField` (400, path `metadata.<key>`) or a 422 `ValidationError`. The `Item` response is a `oneOf`; `httpapi/items.go` encodes `ItemBase` + `kind` + the stored metadata JSON and loads it into the union (no switch over kinds). Prices map `NUMERIC(12,2)` ↔ `"1299.00"` with `big.Int` (D-062), never floats. Item create/move/reorder take the tree lock (`LockTree`), and so must item trash (B-10).
- 2026-10-14: Content (B-08, `internal/content`): the current version is the highest `item_content_versions` row, saves lock the item `FOR NO KEY UPDATE` and never touch `items.version`. The 1 MiB limit counts decoded UTF-8 bytes in the service (413), separate from the guard's 2 MiB JSON limit. Go's JSON decoder turns invalid UTF-8 into U+FFFD, so the 422 for it is only reachable from the service; NUL is rejected too (Postgres `text`). Tests build their router with `newContentEnv` (workspace plus a settings row).
- 2026-10-14: Search (`internal/search`) reads items by title only (`deleted_at IS NULL`, no ancestor check: batch cascade). `q` is trimmed in the service (blank → `search.ErrInvalidQuery` → 400 `invalid_parameter` from the handler; the guard already rejects empty/too long raw `q`) and escaped with `items.EscapeLike`. Paths come from all active folders+lists loaded once per request. The trigram index only kicks in on larger tables for 3+ char terms (seq scan below ~tens of thousands of rows is expected).
- 2026-10-14: Images (B-09, D-064): files are written **before** the tx and deleted **after** commit (the previous attachment *row* goes inside the tx). `images.Process` sniffs magic bytes and checks `DecodeConfig` (40 MP) before decoding. Storage keys are generated (`images/xx/yy/<uuid>.<ext>`), never user-derived. A failed delete only leaves orphan files, which `Service.Sweep` removes after 1 h (injected clock; tests advance it). `nosniff`/CSP on served files come from `securityHeaders`; `Content-Disposition` is added in `httpapi/images.go`. JPEG EXIF orientation is applied to thumbnails by an own parser, PNG/WebP orientation is not.
- 2026-10-15: F005-B1: `Process` stores the original **stripped** (`images.Strip`, lossless: no EXIF/GPS/XMP/IPTC/comments; JPEG keeps ICC/JFIF/Adobe and a minimal orientation-only EXIF), so the stored bytes and `sha256` differ from the upload. Bytes after JPEG EOI / PNG IEND / RIFF end are copied untouched.
- 2026-10-14: Trash (B-10, `internal/trash`, D-065): delete, restore and purge all take the tree lock. Delete marks the root and every *active* descendant with one `trash_batch_id` (folders, then lists in them, then items in the batch's lists); older nested batches keep theirs. Restore clears the root first (parent, `sort_order` appended last, `version + 1`), then the rest of the batch, then deletes the batch row (D-051: never delete a batch row that rows still reference). Purge collects attachment keys of the whole subtree, any batch, hard-deletes the root (FK cascades), deletes the attachment rows and the batch plus batches whose root is gone, commits, then deletes files (failures are logged, the images sweeper retries). Trash queries use `UNION` (not `UNION ALL`) in recursive CTEs so corrupt cycles still terminate. The 501 stub path is gone (`apperr.NotImplemented`, `not_implemented`).

- 2026-10-14: Backup scripts (Q-02, `scripts/lib/backup-common.sh`): never write `-v appdata:/data` for a one-off container, a bare name is a different, global volume (use `appdata_volume`/`carrier`). Containers that must not eat the script's stdin need `</dev/null` (`docker compose run/exec -T` forwards it, which broke the `--force` prompt). The db image's init phase runs a temporary server that answers `pg_isready` on the socket, so wait over TCP (`wait_for_db`).
- 2026-10-15: F-007 (spec 0.3.0): a list has one `kind` and items derive theirs from it (item reads JOIN `lists`; `LockItem`/`GetTrashedItem` carry no kind). Changing a list's kind clears all its items' metadata (trashed ones too) in one tx under the tree lock; moving an item across kinds is 409 `kind_mismatch`. Sort `kind_key` uses `kinds.SortKey` and `ListItemsByKindKey`. List `description` is Markdown, 64 KiB counted in bytes in the service, so list create/update get the 2 MiB JSON guard limit (`guard.go bodyLimit`). Migration 000002 moves dropped fields into a "From 1.0" content version and splits mixed lists; its down is lossy.
- 2026-10-15: F-002 (spec 0.4.0): `streaming.paid_platforms` is a parallel name list (subset of `platforms`); motorcycle/camera have no make/model (migration 000003 moved them to a "From 1.1" content block); `lists.pinned_fields` is validated by `kinds.ValidatePinned` against `kinds.PinnableKeys` (kind keys + `price`) and reset by the list-kind change; sort mode `price` orders by `price_min` (no currency conversion), no-price last.
- 2026-10-16: F-009 (spec 0.7.0): `kinds.HasPrice` (motorcycle, car, camera) is the single source of truth for price. F008-B1's "generic" price rule is generalised: a non-null price on a generic/movie/tv_show item is 422 on `price`, `?sort=price` on those lists is 400 `invalid_parameter`, and a list of such a kind cannot pin `price` nor store `sort_mode: price`. Migration 000007 moves their prices to a "From 1.3" content block. `lists.pinned_fields` is now an ordered array of at most 2 keys, the first primary type (migration 000008; no more `{under, right}`, `kinds.ValidatePinned` errors on `pinned_fields[i]` / `pinned_fields`).
- 2026-10-16: F-012 (spec 0.8.0): `lists.icon` is an emoji (own grammar in `internal/tree/icon.go`, no grapheme library) or `i:<name>` (the server does not know the built-in set); `icon_color` (10 palette names) only with an `i:` icon and is cleared when the icon becomes an emoji/null. `DELETE /trash` = `trash.PurgeAll`. `workspace_settings.trash_retention_days` (null = off): `trash.RunAutoPurge` runs at start-up and daily, tree lock, files deleted after commit. Migrations 000009/000010 downs are lossy.
- 2026-10-19: The concept "kind" is now "type" everywhere (spec 0.11.0, breaking: `/kinds` -> `/types`, `kind` -> `type` on lists/items/queries, `kind_mismatch` -> `type_mismatch`, `kind_name` -> `type_name`, package `items/kinds` -> `items/types`, Go locals `typ`). Migration 000017 renames `kind_definitions`/`kind_fields`/`kind_id`, `lists.kind`, `tmdb_imports.kind`, their constraints/indexes and the seed functions/trigger (`seed_builtin_type_definitions`, `apply_builtin_type_default_pins`); the `field_type` column and a field definition's `type` property are unchanged. Older gotcha entries above keep the old "kind" wording: read it as "type".
- 2026-10-19: API 0.11.0 (migration 000018): the built-in `generic` type is gone. A list with no type has `type` NULL (`lists.type` nullable, `tree.List.Type`/`items.Item.Type` are `*string`, `ListPatch.Type` an `Optional`), its items' `type` is `null` and `metadata` must be `{}`; no definition, so no fields, no price, built-in sort modes only, no pins. `items.Service.ValidateMetadata(nil)` and `tree.typeFields(nil)` are that no-type path. Moves: nil vs nil is fine, nil vs typed is `type_mismatch`. `generic` is an ordinary key now (a custom type may be named so; down migration folds it back). Search `type_name` is nullable. `types.sql` params use `sqlc.arg(type)::text` to keep them non-pointer.

## Adding an endpoint (B-05+)
1. Implement the operation's method in a file of its own (`settings.go`, `folders.go`, `trash.go`, …) on `*Server` (the generated strict interface has no stub left: a missing method is a compile error). Add the service dependency to `Deps`/`Server`.
2. Return domain errors from `internal/apperr` (`return nil, apperr.NotFound("item")`), never build 4xx/5xx problems by hand: `ResponseErrorHandlerFunc` maps them (`problem.go`). Typed `…ApplicationProblemPlusJSONResponse` values stay allowed.
3. `If-Match` operations: read the version with `httpapi.IfMatchVersion(ctx)`, compare it in the service (stale → `apperr.VersionConflict(current)`), and set `ETag` with `etag.Format(etag.Item, v)`. Creates also set `Location`.
4. Always set every `nullable.Nullable` response field (`NewNullNullable[T]()` or `NewNullableWithValue`): unset ones marshal as the zero value, not `null`.
5. Multi-statement writes use `database.InTx`; time comes from the injected `Clock` (`Deps.Clock`).
6. Test through `httpapi.NewRouter` with real Postgres; table-driven, named after the acceptance test.

## Working guidelines

### Shell
- Check the parent directory exists before creating files or directories. Quote paths with spaces (`"path with spaces/file"`).
- Prefer absolute paths over `cd` (e.g. `go test /abs/path/...`). Chain commands with `&&` or `;`, not newlines.
- Use the Read/Grep/Glob tools instead of `cat`/`grep`/`find`/`ls`. If shell search is unavoidable, use `rg`.
- Give every command a clear 5–10 word description. Use `run_in_background` for long-running processes, never `sleep`.

### Proactiveness
- Do what was asked, including natural follow-ups (tests, docs, AGENTS.md), but don't surprise the user with unrequested actions. If asked *how*, answer first.
- **Never guess.** Don't guess that a method exists, an object's type, that tests pass, or business-logic flows. Verify in code or docs, or ask.

### Conventions
- Mimic the surrounding code style, reuse existing helpers, and read imports before editing.
- Never assume a library is available. Check `go.mod` first, and justify any new dependency in the PR.
- Security: never log or commit secrets or keys. `.env` is gitignored, and `.env.example` holds placeholders only.
