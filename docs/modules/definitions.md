# definitions

## Purpose

Owns workspace type definitions and their custom fields: list types, and create, rename, reorder, preview and delete custom fields (Huma, `/api/v1/types...`). Types are fixed: the five built-in types are seeded locked by [seed](seed.md), and there are no create, update or delete type operations (F-022). Type rules: `/Users/federico/Developer/collections-app/design-docs/` (DATA_MODEL §2, F-018, F-022).

## Key files

- `internal/definitions/service.go`: `Service`, `Store`/`Repo` interfaces, field rules and transactions
- `internal/definitions/validate.go`: pure helpers (field label normalising, field key derivation, key sets), no I/O
- `internal/definitions/repository.go`: `Postgres`, `repo`; one method per query over sqlc
- `internal/definitions/model.go`: `Type`, `Field`, `CreateField`
- `internal/definitions/handler.go`, `wire.go`: Huma operations and wire shapes
- `db/queries/types.sql`: the queries behind `repository.go`

## Interfaces

- `NewPostgres(db DB) *Postgres`; `NewService(store Store, clk clock.Clock) *Service`
- `Service.List(ctx) ([]Type, error)`; `CreateField`, `RenameField`, `ReorderFields` (returns the new type version), `FieldDeletePreview`, `DeleteField`
- `Register(api huma.API, log, svc *Service)`: mounts the operations below
- `GET /api/v1/types`, `POST /api/v1/types/{key}/fields`, `PUT .../fields/order`, `GET`/`PATCH`/`DELETE .../fields/{fieldKey}`

## Gotchas

- Every write runs in one transaction that locks the type row (and the field row for field edits) before the version check, so a stale `If-Match` writes nothing.
- There is no type create, update or delete, and no `GET` for one type. Locked built-in types and their fields cannot be changed.
- Custom field keys derive from the label (lowercase letters, digits, underscores).
- Reorder accepts exactly the custom-field keys (else 409 `order_stale`). It parks fields above the current maximum, then sets final positions, because the `(type, position)` unique constraint is not deferrable.
- `stores` is a locked-only field type (DB CHECK); custom fields cannot use it.
- Deleting a custom field strips matching item metadata, resets the type's default sort and the list sorts that named it, and counts trashed items too.
- Migration 000021 renames a custom `book` to `book_custom`; 000025 does the same for `vehicle` and `wishlist`. Their lists follow. The no-type list has no definition at all.
