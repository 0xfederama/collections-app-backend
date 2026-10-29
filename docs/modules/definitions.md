# definitions

## Purpose

Owns workspace type definitions and their fields: list, create, update and delete types, and create, rename, reorder, preview and delete custom fields (Huma, `/api/v1/types...`). Built-in types and fields are seeded locked by [seed](seed.md) and cannot be edited or deleted. Type rules: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §2, F-018).

## Key files

- `internal/definitions/service.go`: `Service`, `Store`/`Repo` interfaces, type and field rules and transactions
- `internal/definitions/validate.go`: pure helpers (keys, labels, icon, default checks), no I/O
- `internal/definitions/repository.go`: `Postgres`, `repo`; one method per query over sqlc
- `internal/definitions/model.go`: `Type`, `Field`, `UpdateType`, `NullableString(s)`
- `internal/definitions/handler.go`, `wire.go`: Huma operations and wire shapes
- `db/queries/types.sql`: the queries behind `repository.go`

## Interfaces

- `NewPostgres(db DB) *Postgres`; `NewService(store Store, clk clock.Clock) *Service`
- `Service.List`, `Create`, `Update(ctx, key, version, UpdateType)`, `Delete(ctx, key, version)`
- `Service.CreateField`, `RenameField`, `ReorderFields` (returns the new type version), `FieldDeletePreview`, `DeleteField`
- `Register(api huma.API, log, svc *Service)`: mounts the type and field operations
- `GET/POST /api/v1/types`, `PATCH/DELETE /api/v1/types/{key}`, `POST /api/v1/types/{key}/fields`, `PUT .../fields/order`, `GET/PATCH/DELETE .../fields/{fieldKey}`

## Gotchas

- Every write runs in one transaction that locks the type row (and the field row for field edits) before the version check, so a stale `If-Match` writes nothing.
- Keys derive from names and labels (lowercase letters, digits, underscores). `store_links` is a reserved field key.
- Locked types and fields cannot be changed. A type used by an active list cannot be deleted (422 names the first list). Delete removes the fields before the type row.
- Reorder accepts exactly the custom-field keys (else 409 `order_stale`). It parks fields above the current maximum, then sets final positions, because the `(type, position)` unique constraint is not deferrable.
- Default icon uses the list icon grammar (`lists.ValidIcon`), so this package imports `internal/lists`, not the reverse. `UpdateType` tells an omitted default from an explicit `null`.
- Defaults (sort, pins) must name an existing field; `price` only when `allows_price`. Pins hold at most three unique keys. A `stores` field is pinnable but never a sort key.
- `stores` is a locked-only field type (DB CHECK); custom fields cannot use it.
- Deleting a custom field strips matching item metadata, resets affected defaults, pins and list sorts, and counts trashed items too.
- Migration 000021 renames a custom `book` to `book_custom`; 000025 does the same for `vehicle` and `wishlist`. Their lists follow. The no-type list has no definition at all.
