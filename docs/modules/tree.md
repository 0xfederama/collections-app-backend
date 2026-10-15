# tree

## Purpose

Owns folders and lists of the single workspace: create, read, rename, move, reorder and the flat sidebar tree. Every list has one required `kind` that all its items share (F-007). No HTTP; returns `apperr` errors. Deleting is in `internal/trash`. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 and §7).

## Key files

- `internal/tree/tree.go`: `Service`, `Tree`, row mapping, `lockTree`, name, enum and description validation
- `internal/tree/folders.go`: folder create, get, rename, move (cycle check), reorder
- `internal/tree/lists.go`: list create, get with path, patch (kind change), move, reorder
- `db/queries/folders.sql`, `lists.sql`, `tree.sql`: sqlc inputs (`LockTree` is in `tree.sql`)
- `internal/httpapi/tree_test.go`: AT-03, AT-04, AT-21 and 404/409/412/422 cases

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Tree(ctx) (Tree, error)`: all active folders and lists, ordered by `(sort_order, id)`
- `CreateFolder`, `GetFolder`, `UpdateFolder(ctx, id, version, name *string)`, `MoveFolder(ctx, id, version, parent *uuid.UUID)`: nil parent is root
- `ReorderFolders(ctx, parent *uuid.UUID, ids []uuid.UUID) error`
- `CreateList(ctx, CreateListInput) (List, error)`: `Kind` required; empty modes take the defaults
- `GetList(ctx, id) (List, error)`: fills `Path` (ancestors, root first)
- `UpdateList(ctx, id, version, ListPatch) (List, error)`: a `Kind` change clears the items' metadata
- `MoveList(ctx, id, version, folder *uuid.UUID) (List, error)`, `ReorderLists(ctx, folder *uuid.UUID, ids []uuid.UUID) error`
- `PinnedFields{Under, Right}`: nil = kind default; validated by `kinds.ValidatePinned`
- `Optional[T]` (`Set`, `Value`): PATCH field that tells absent from null

## Gotchas

- Every read and write sees active rows only; a missing or trashed resource is 404.
- Structural writes (create, move, reorder) take `LockTree` (one `pg_advisory_xact_lock` per workspace) first; the folder cycle check relies on it. Trash delete and restore must take it too.
- Create and move append last (`max(sort_order)+1` among active siblings). Reorder rewrites `sort_order` to 0..n-1 without bumping versions; `ids` must equal the active siblings, else 409 `order_stale`.
- Move checks in order: 404, 412 `version_conflict`, 409 `destination_unavailable`, 409 `cycle_detected`.
- A PATCH with no fields succeeds and does not bump the version. A `kind` equal to the stored one is a no-op.
- A kind change runs in one transaction under the tree lock: it updates the list, resets `pinned_fields` to NULL and clears its items' metadata (trashed ones too, bumping their versions). A `pinned_fields` sent in the same PATCH is kept.
- `pinned_fields` is `{under, right}` (JSONB, migration 000004). Keys are the kind's metadata keys plus `price`; `generic` pins nothing. At most 6 per slot, one slot per key (422 on `pinned_fields.under[i]`).
- `sort_mode: kind_key` is 422 on `generic`, and so is a change to `generic` while on `kind_key`. `sort_mode: price` is 422 on `generic`. A change to `generic` clears the items' price (trashed too) and turns a stored `price` sort into `manual` (F008-B1).
- Description is Markdown, at most 64 KiB (413 over); invalid UTF-8 or NUL is 422. Name is trimmed to 1-200 characters.
