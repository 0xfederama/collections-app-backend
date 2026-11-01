# lists

## Purpose

Owns lists: create, get (with ancestor folder path), patch, move and reorder. A list has one optional type (`nil` = no type: no fields, no price, built-in sorts). A given type must exist in the workspace; it governs the default sort and the sort options for its items. Lists have no pins (F-022). Type changes clear the items' metadata in the same transaction. Deleting lists is in `internal/trash`. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2).

## Key files

- `internal/lists/service.go`: `Service`, `Store`/`Repo`, `TypeSource`, create, get, update, move, reorder
- `internal/lists/plan.go`: pure `planUpdate`, decides what a PATCH writes (no I/O)
- `internal/lists/validate.go`: name, description, enum, type and sort checks
- `internal/lists/icon.go`: icon grammar (one emoji or `i:<name>`), palette, `ValidIcon`
- `internal/lists/repository.go`: `Postgres`, `repo`; item-clear queries are the seams (B3d)
- `internal/httpapi/router.go`: `listTypes`, the `TypeSource` adapter over [definitions](definitions.md)
- `internal/lists/handler.go`, `wire.go`: Huma operations, `ListJSON`, `ListWithPathJSON`

## Interfaces

- `NewService(store Store, types TypeSource, clk clock.Clock) *Service`; `NewPostgres(db DB) *Postgres`; `TypeSource.TypeInfo(ctx, key *string)` (nil = no type, wired in `httpapi`)
- `Service.Create(ctx, CreateInput) (List, error)`; `Get(ctx, id) (List, error)` fills `Path` (root first)
- `Service.Update(ctx, id, version, Patch) (List, error)`: `Optional` fields tell absent from `null`
- `Service.Move(ctx, id, version, folder *uuid.UUID)`; `Reorder(ctx, folder *uuid.UUID, ids []uuid.UUID) error`
- `ValidIcon(icon string) bool`: exported icon check; no caller outside `internal/lists` after F-022
- `Postgres.ListActive(ctx) ([]List, error)`: used by `internal/tree`, fills `ItemCount`
- `POST /api/v1/lists` (201), `GET`/`PATCH /api/v1/lists/{id}`, `POST /api/v1/lists/{id}/move`, `PUT /api/v1/lists/order` (204)

## Gotchas

- Create, move and reorder take `LockTree` first; a PATCH that sends `type` takes it too, before the row lock.
- Update and move need `If-Match` (`"l-N"`); a stale version is 412 `version_conflict`. A PATCH that changes nothing writes nothing and keeps the version.
- Unknown type on create or update is 422 on `type`; a blank type key is 422. The type is read before the transaction, so no second pool connection is held while the tree lock is taken.
- A type change clears all items' metadata (trashed ones too) and their price when the new type has none. Same type sent again is no change.
- Sort on a type change: the patch's sort, else the new type's default sort field, else `manual`. An invalid stored sort falls back to `manual`.
- Sort is `manual`, `title`, `updated`, `price` (only if the type allows it) or a field key (never a `stores` field).
- `icon_color` needs a built-in icon (else 422); changing to an emoji or none clears it.
- Description is Markdown: over 64 KiB is 413, invalid UTF-8 or NUL is 422, empty clears it. Name is trimmed to 1 to 200 characters.
- Move: 404, 412, 409 `destination_unavailable`. Create with a missing or trashed folder is also 409.
- Reorder: a missing folder is 404; `ids` must be exactly the active child lists, else 409 `order_stale`.
- `GET` path is empty for a root list; `ItemCount` is filled only by `Get` and `ListActive`.
