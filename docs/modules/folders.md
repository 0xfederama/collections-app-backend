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
- `Service.Reorder(ctx, parent *uuid.UUID, ids []uuid.UUID) error`: at the root, the caller's own order (`SetTopOrder`)
- `WithAccess(a Access) *Service` (`Access`: `FolderVisible`, `VisibleFolderIDs`, see [access](access.md)); `FolderJSON.owner_user_id`, top-level only
- `Postgres.ListActive(ctx) ([]Folder, error)`: used by `internal/tree`
- `AddMember(ctx, id, email)`, `RemoveMember(ctx, id, user)`; `WithUsers(u Users)` (`IDByEmail`, wired to `users.Service.Lookup`); `FolderJSON.shared`, `member_user_ids` (empty for nested or unshared)
- `POST /api/v1/folders/{id}/members` (200 Folder), `DELETE /api/v1/folders/{id}/members/{user_id}` (204), both under `LockTree`, no `If-Match`, no version bump
- `POST /api/v1/folders` (201, `Location`), `GET`/`PATCH /api/v1/folders/{id}`, `POST /api/v1/folders/{id}/move`, `PUT /api/v1/folders/order` (204)

## Gotchas

- Create, move and reorder take `LockTree` first (one advisory lock per workspace, D-060); the cycle check relies on it. Rename does not.
- Rename and move need `If-Match` (`"f-N"`); a stale version is 412 `version_conflict`, a missing header 428.
- Move order: 404, 412, 409 `destination_unavailable` (missing or trashed parent), 409 `cycle_detected` (into its own subtree).
- Create with a missing or trashed parent is 409 `destination_unavailable`. A parent the caller cannot see gets the same answer; the check runs before the transaction.
- Reorder: `ids` must be exactly the active child folders, else 409 `order_stale`; a missing, trashed or invisible parent is 404. Below a folder it rewrites the shared `sort_order` to 0..n-1 and bumps no versions.
- Root reorder is per user: only the caller's visible top-level folders are compared, and only their own `user_top_order` rows are written; other users' positions and the shared `sort_order` stay.
- Owner: create sets the caller as owner only at the top level; a move into a folder clears it, a move to the top keeps the stored owner or takes the caller's (`folders.sql`).
- Move and reorder bodies require `parent_folder_id` (`null` = root); absent is a validation error.
- Rename with no `name` changes nothing and does not bump the version. Names are trimmed to 1 to 200 characters (422 otherwise).
- Cycle walk is capped at the folder count, so corrupt parent chains still terminate.
- Share order of errors: 404 (guard, or not visible in the tx), 409 `folder_not_top_level`, 403 `forbidden` (not the owner), 422 on `email` (unknown user, or the owner). Already a member: 200 unchanged. Unshare: owner removes anyone, a member only themself (403 otherwise), not a member 404; it also deletes that user's `user_top_order` row and works on a trashed folder.
- Move: 404, 403 `forbidden` (top-level folder the caller does not own), 412, 409 `destination_unavailable`, 409 `folder_shared` (a folder with members into a folder), 409 `cycle_detected`. The folder and the destination are re-checked for visibility inside the tx.
