# workspace

## Purpose

Owns the single MVP workspace: an idempotent bootstrap at startup (`WORKSPACE_NAME` applies only on first boot) and the workspace settings read and PATCH, with optimistic locking on `workspace_settings.version`. Data rules: `/Users/federico/Developer/collections-app/design-docs/DATA_MODEL.md` §2/§3, API.md §5.

## Key files

- `internal/workspace/bootstrap.go`: `Bootstrap`, advisory lock, built-in definition seed
- `internal/workspace/settings.go`: `Service`, `Get`, `Update`, validation, version check
- `db/queries/workspace.sql`: bootstrap, settings get/lock/update queries
- `internal/database/sqlc/workspace.sql.go`: generated, never edit
- `internal/workspace/workspace_test.go`, `settings_test.go`: bootstrap and settings tests
- `internal/httpapi/settings.go`: GET/PATCH `/api/v1/settings` handlers; adds read-only `book_lookup_available`

## Interfaces

- `Bootstrap(ctx, db database.TxBeginner, name string) (uuid.UUID, error)`: creates a workspace with settings and built-in definitions, or returns its ID
- `NewService(db *pgxpool.Pool) *Service`
- `(*Service).Get(ctx, workspaceID) (Settings, error)`: `apperr.NotFound("settings")` if no row
- `(*Service).Update(ctx, workspaceID, ifMatch int, p SettingsPatch) (Settings, error)`: 422 field errors, then 412 `apperr.VersionConflict` on a stale version
- `Settings`: workspace name, streaming country, `MetadataLanguage`, markdown history limit, `TrashRetentionDays *int` (nil = off), `PreferredStores []string`, version
- `SettingsPatch`: pointer fields; nil means "leave as is"; `TrashRetentionDays` is `Optional[int]` (absent vs explicit null); `PreferredStores *[]string`

## Gotchas

- `Bootstrap` runs from `cmd/api/main.go` before the server starts. It takes `pg_advisory_xact_lock` with a fixed key; any other bootstrap-like task needs a different key.
- A later `WORKSPACE_NAME` does not rename an existing workspace.
- More than one workspace is an error (the MVP supports exactly one).
- Bootstrap seeds built-in definitions only when it creates the workspace; migration 000014 seeds existing workspaces.
- Validation runs before the row lock, so an invalid body answers 422 even when If-Match is stale.
- A patch that changes nothing returns the current settings with no version bump.
- A name change updates `workspaces.name` in the same transaction; it bumps `workspaces.version`, but the ETag stream is only `workspace_settings.version` (`"s-N"`).
- `Update` writes in `database.InTx` and locks the settings row `FOR UPDATE`.
- `trash_retention_days` (1 to 365, or null = off; migration 000010, default NULL) is read by the trash auto-purge (`internal/trash`). Setting it purges nothing at once.
- `preferred_stores` (migration 000020, default empty) is an ordered list of unique provider keys from `internal/stores`, at most six; order is kept and used when items are created.
- `book_lookup_available` is not stored: it is true when the Open Library client is enabled, which is always (no key or setting).
- `metadata_language` defaults to `it-IT` for new workspaces; it accepts a lowercase ISO 639-1 code with optional uppercase region (`xx` or `xx-YY`) and is used for TMDB results.
- Settings equality (`settings.equal`) compares the retention by value, so repeating the stored number changes nothing.
