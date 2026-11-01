# workspace

## Purpose

Owns the single MVP workspace: an idempotent bootstrap at startup (`WORKSPACE_NAME` applies only on first boot) and the workspace settings read and PATCH (`GET`/`PATCH /api/v1/settings`, on Huma), with optimistic locking on `workspace_settings.version`. Data rules: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md` §2/§3, API.md §5.

## Key files

- `internal/workspace/bootstrap.go`: `Bootstrap`, advisory lock, calls `seed.BuiltinTypes`
- `internal/workspace/model.go`: `Settings`, `SettingsPatch`
- `internal/workspace/service.go`: `Service`, `Get`, `Update`, validation, version check
- `internal/workspace/repository.go`: `Postgres` store, scoped queries, settings row mapping
- `internal/workspace/handler.go`: Huma GET/PATCH settings, adds read-only `book_lookup_available`
- `db/queries/workspace.sql`: bootstrap, settings get/lock/update queries
- `internal/platform/database/sqlc/workspace.sql.go`: generated, never edit
- `internal/workspace/workspace_test.go`, `settings_test.go`, `service_test.go`, `settings_huma_test.go` (httpapi): tests

## Interfaces

- `Bootstrap(ctx, db database.TxBeginner, name string) (uuid.UUID, error)`: creates the workspace with settings if missing, runs `seed.BuiltinTypes` on every call, returns its ID
- `NewPostgres(pool *pgxpool.Pool) *Postgres`, `NewService(store Store) *Service`: the store is `InTx` plus `Get`
- `(*Service).Get(ctx) (Settings, error)`: workspace from `scope.WorkspaceID(ctx)`; `apperr.NotFound("settings")` if no row
- `(*Service).Update(ctx, ifMatch int, p SettingsPatch) (Settings, error)`: 422 field errors, then 412 `apperr.VersionConflict` on a stale version
- `Register(api, log, svc, reader, Availability)`: adds the two settings operations to a Huma API
- `Settings`: name, streaming country, `MetadataLanguage`, markdown history limit, `TrashRetentionDays *int` (nil = off), `PreferredStore`, `OwnedStreamingServices []string` (never nil, `[]` = show all), version
- `SettingsPatch`: pointer fields (nil = leave as is); `TrashRetentionDays` is `Optional[int]`

## Gotchas

- `Bootstrap` runs from `cmd/api/main.go` before the server starts, under `pg_advisory_xact_lock` with a fixed key; a later `WORKSPACE_NAME` does not rename an existing workspace. More than one workspace is an error.
- Built-in types are seeded on every start, not only on creation (idempotent, see `seed`). No SQL trigger seeds them.
- Validation runs before the row lock, so an invalid body answers 422 even when If-Match is stale.
- A patch that changes nothing returns the current settings with no version bump. Equality compares retention by value.
- A name change updates `workspaces.name` in the same transaction and bumps its version; the ETag stream is only `workspace_settings.version` (`"s-N"`).
- `Update` runs in `database.InTx` and locks the settings row `FOR UPDATE`. Repository methods never take a workspace ID; they read it from the context.
- Changing `streaming_country_code` also clears `streaming.link` on items and snapshots (`ClearItemStreamingLinks`, `ClearStreamingLinkSnapshots`), workspace-wide, in the same transaction. No item version bump.
- `trash_retention_days` (1 to 365, or null = off; default NULL) is read by the trash auto-purge (`internal/trash`). Setting it purges nothing at once.
- `preferred_store` (NOT NULL, default `amazon`, CHECK on the five store keys) seeds `stores` for new book items and book lookup. An unknown key is 422 on `preferred_store`.
- `owned_streaming_services` (`jsonb` array, NOT NULL, default `[]`, CHECK array of at most 64) is the user's streaming services as provider keys (F-022 B3). The web registry owns the key list, so the backend checks only the shape and rejects duplicates (422). Absent in a PATCH = unchanged, `[]` = show all.
- `book_lookup_available` is not stored: it is true when the Open Library client is enabled, which is always.
- `metadata_language` defaults to `it-IT` for new workspaces; it takes `xx` or `xx-YY` and is used for TMDB results.
