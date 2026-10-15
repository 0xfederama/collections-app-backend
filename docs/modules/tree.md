# tree

## Purpose

Owns folders and lists of the single workspace: create, read, rename, move, reorder and the flat sidebar tree. Every list has one required `kind` that all its items share (F-007). No HTTP; returns `apperr` errors. Deleting folders and lists is in `internal/trash`. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 and §7).

## Key files

- `internal/tree/tree.go`: `Service`, `Tree`, row mapping, `lockTree`, name, enum and description validation
- `internal/tree/folders.go`: folder create, get, rename, move (cycle check), reorder
- `internal/tree/lists.go`: list create, get with path, patch (kind change), move, reorder
- `db/queries/folders.sql`, `lists.sql`, `tree.sql`: sqlc inputs (`LockTree` lives in `tree.sql`)
- `internal/httpapi/tree_test.go`: AT-03, AT-04, AT-21 and 404/409/412/422 cases

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Tree(ctx) (Tree, error)`: all active folders and lists, ordered by `(sort_order, id)`
- `CreateFolder(ctx, name, parent *uuid.UUID) (Folder, error)`
- `GetFolder(ctx, id) (Folder, error)`
- `UpdateFolder(ctx, id, version, name *string) (Folder, error)`
- `MoveFolder(ctx, id, version, parent *uuid.UUID) (Folder, error)`: nil parent is root
- `ReorderFolders(ctx, parent *uuid.UUID, ids []uuid.UUID) error`
- `CreateList(ctx, CreateListInput) (List, error)`: `Kind` required; empty modes take `DefaultDisplayMode` / `DefaultSortMode`
- `GetList(ctx, id) (List, error)`: fills `Path` (ancestors, root first)
- `UpdateList(ctx, id, version, ListPatch) (List, error)`: `Kind` change clears the items' metadata
- `MoveList(ctx, id, version, folder *uuid.UUID) (List, error)`
- `ReorderLists(ctx, folder *uuid.UUID, ids []uuid.UUID) error`
- `Optional[T]` (`Set`, `Value`): PATCH field that tells absent from null

## Gotchas

- Every read and write sees active rows only; a missing or trashed resource is NotFound (404).
- Structural writes (create, move, reorder) take `LockTree` (one `pg_advisory_xact_lock` per workspace) first. It serializes the tree, which makes the folder cycle check race-free. Trash delete and restore must take it too.
- Create and move append last (`max(sort_order)+1` among active siblings). Reorder rewrites `sort_order` to 0..n-1 without bumping versions.
- Reorder `ids` must equal the active siblings as a set, else 409 `order_stale`. A missing or trashed parent is 404.
- Move checks in order: 404, 412 `version_conflict`, 409 `destination_unavailable`, 409 `cycle_detected` (walks up from the destination).
- A PATCH with no fields succeeds and does not bump the version. A `kind` equal to the stored one is a no-op.
- A kind change runs under the tree lock: it updates the list and clears the metadata of its items (trashed ones too, which bumps their versions) in one transaction.
- `kind` is one of the six kinds (422 otherwise). `sort_mode: kind_key` is 422 on a `generic` list, and so is a change to `generic` while the list is on `kind_key`.
- Description is Markdown, at most 64 KiB (413 over); invalid UTF-8 or NUL is 422. Name is trimmed to 1-200 characters; enum violations are 422.
