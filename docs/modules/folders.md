# folders

## Purpose

Owns workspace folders: create, get, rename, move (cycle-checked) and reorder the child folders of a parent (`nil` = root). Structural writes take the tree lock. Deleting folders is in `internal/trash`. Huma routes are registered from this package; design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5).

## Key files

- `internal/folders/service.go`: `Service`, `Store`/`Repo` interfaces, cycle check, reorder, `cleanName`
- `internal/folders/repository.go`: `Postgres` store and per-transaction `repo` over sqlc
- `internal/folders/handler.go`: Huma operations, `If-Match` parsing, `ETag`/`Location`
- `internal/folders/wire.go`: `FolderJSON`, `ToJSON` (also used by `tree`)
- `internal/folders/model.go`: `Folder` domain type
- `db/queries/folders.sql`: sqlc queries (`LockFolder`, `ListActive`, sort order)

## Interfaces

- `NewService(store Store, clk clock.Clock) *Service`; `NewPostgres(db DB) *Postgres`
- `Service.Create(ctx, name, parent *uuid.UUID) (Folder, error)`: appended last among siblings
- `Service.Get(ctx, id)`, `Rename(ctx, id, version, name *string)`, `Move(ctx, id, version, parent *uuid.UUID)`
- `Service.Reorder(ctx, parent *uuid.UUID, ids []uuid.UUID) error`
- `Postgres.ListActive(ctx) ([]Folder, error)`: used by `internal/tree`
- `POST /api/v1/folders` (201, `Location`), `GET`/`PATCH /api/v1/folders/{id}`, `POST /api/v1/folders/{id}/move`, `PUT /api/v1/folders/order` (204)

## Gotchas

- Create, move and reorder take `LockTree` first (one advisory lock per workspace, D-060); the cycle check relies on it. Rename does not.
- Rename and move need `If-Match` (`"f-N"`); a stale version is 412 `version_conflict`, a missing header 428.
- Move order: 404, 412, 409 `destination_unavailable` (missing or trashed parent), 409 `cycle_detected` (into its own subtree).
- Create with a missing or trashed parent is 409 `destination_unavailable`.
- Reorder: `ids` must be exactly the active child folders, else 409 `order_stale`; a missing or trashed parent is 404. It rewrites `sort_order` to 0..n-1 and bumps no versions.
- Move and reorder bodies require `parent_folder_id` (`null` = root); absent is a validation error.
- Rename with no `name` changes nothing and does not bump the version. Names are trimmed to 1 to 200 characters (422 otherwise).
- Cycle walk is capped at the folder count, so corrupt parent chains still terminate.
