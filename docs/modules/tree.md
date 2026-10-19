# tree

## Purpose

Owns workspace folders and lists: create, read, patch, move, reorder and the sidebar tree. A list type is optional: `nil` is a list with no type (API `type: null`, no fields, no price, built-in sort modes only, no pins). A given type must exist in the workspace definitions; it governs defaults, pins, sorting and its items. No HTTP; deletion is in `internal/trash`.

## Key files

- `internal/tree/tree.go`: `Service`, `Tree`, row mapping, `lockTree`, name, enum and description validation
- `internal/tree/folders.go`: folder create, get, rename, move (cycle check), reorder
- `internal/tree/lists.go`: definition-backed list create and patch, `validateDefinitionPins`, move, reorder
- `internal/tree/icon.go`: icon grammar (one emoji or `i:<name>`), colour palette, `iconErrors`
- `db/queries/folders.sql`, `lists.sql`, `tree.sql`: sqlc inputs (`LockTree` is in `tree.sql`)

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Tree(ctx) (Tree, error)`: all active folders and lists, ordered by `(sort_order, id)`
- `CreateFolder`, `GetFolder`, `UpdateFolder(ctx, id, version, name *string)`, `MoveFolder(ctx, id, version, parent *uuid.UUID)`: nil parent is root
- `ReorderFolders(ctx, parent *uuid.UUID, ids []uuid.UUID) error`
- `CreateList(ctx, CreateListInput) (List, error)`: nil `Type` = no type; a given type must be a workspace type; definition default sort and pins apply
- `GetList(ctx, id) (List, error)`: fills `Path` (ancestors, root first)
- `UpdateList(ctx, id, version, ListPatch) (List, error)`: `Type` is an `Optional` (Set with nil Value = no type); a change, also to or from no type, clears the items' metadata
- `MoveList(ctx, id, version, folder *uuid.UUID) (List, error)`, `ReorderLists(ctx, folder *uuid.UUID, ids []uuid.UUID) error`
- `PinnedFields` (ordered `[]string`): validates against the type fields; nil = definition default
- `CreateListInput.Icon`/`IconColor`, `ListPatch.Icon`/`IconColor` (`Optional[string]`): nil or null = default icon
- `Optional[T]` (`Set`, `Value`): PATCH field that tells absent from null

## Gotchas

- Every read and write sees active rows only; a missing or trashed resource is 404.
- Structural writes (create, move, reorder) take `LockTree` (one `pg_advisory_xact_lock` per workspace) first; the folder cycle check relies on it. Trash delete and restore must take it too.
- Create and move append last (`max(sort_order)+1` among active siblings). Reorder rewrites `sort_order` to 0..n-1 without bumping versions; `ids` must equal the active siblings, else 409 `order_stale`.
- Move checks in order: 404, 412 `version_conflict`, 409 `destination_unavailable`, 409 `cycle_detected`.
- A PATCH with no fields succeeds and does not bump the version. A `type` equal to the stored one is a no-op.
- A type change clears metadata (including trashed items), resets pins unless supplied, and clears prices when the destination does not allow them.
- Sort is manual/title/updated, price only when allowed, or a field key (never a `stores` field); pins are an ordered list of at most three unique field keys (or allowed `price`), the first primary.
- Description is Markdown, at most 64 KiB (413 over); invalid UTF-8 or NUL is 422. Name is trimmed to 1-200 characters.
- Icon: one emoji (`icon.go` grammar, max 32 bytes, no grapheme library) or `i:<name>`, else 422 on `icon`. `icon_color` (ten palette names) needs a built-in icon, else 422. An icon change to an emoji or none clears the colour; a colour sent with an emoji is 422.
