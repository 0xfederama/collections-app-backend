# items

## Purpose

Owns items of a list: create, get with path, paged list, partial update, move to another list and reorder within a list. Kind-specific metadata is checked by `items/kinds`. No HTTP; returns `apperr` errors. Item delete (B-10), image writes (B-09), content (B-08) and search (B-11) are not here yet. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 and §7, D-062).

## Key files

- `internal/items/items.go`: `Service`, `Item` read model, `itemFromRow`, `Get`, `List`, `lockTree`
- `internal/items/write.go`: `Create`, `Update` (`mergePatch`), `Move`, `Reorder`, field rules
- `internal/items/price.go`: NUMERIC(12,2) to `"1299.00"` mapping (`big.Int`), price rules
- `internal/items/kinds/kinds.go`: per-kind field registry, `Validate`, `Known`
- `db/queries/items.sql`: sqlc inputs; `GetItem` and `ListItems` share columns
- `internal/httpapi/items.go`: item handlers and body mapping
- `internal/items/price_test.go`, `kinds/kinds_test.go`, `internal/httpapi/items_test.go`: tests

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Get(ctx, id) (Item, error)`: active item with `Path` (folders root first, then the list)
- `List(ctx, listID, ListParams) (ItemPage, error)`: limit 50 default, 200 max; `Total` from a separate count
- `Create(ctx, listID, CreateInput) (Item, error)`: appends last; empty kind is `generic`
- `Update(ctx, id, version, Patch) (Item, error)`: `version` is the If-Match value
- `Move(ctx, id, version, dest) (Item, error)`: appends last in the destination list
- `Reorder(ctx, listID, ids) error`: `ids` must be the active items of the list
- `kinds.Validate(kind, map[string]any) (map[string]any, error)`: normalized metadata to store
- `kinds.Known(kind) bool`; kind constants `Generic`, `Movie`, `TVShow`, `Motorcycle`, `Car`, `Camera`

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) first. `Update` does not.
- Update order: field rules (422) run before the lookup; then 404, 412 `version_conflict`, then metadata (400 or 422).
- Update with no real change returns the current version (no bump). `metadata` replaces the whole object; a kind change without `metadata` resets it to `{}`.
- Move order: 404, 412, then 409 `destination_unavailable` (destination must be an active list).
- Reorder: `ids` must equal the active items as a set, else 409 `order_stale`; a missing list is 404; no version bumps.
- List default sort is the list's `sort_mode`. `manual` uses `sort_order`, `title` is case-insensitive, `updated` is newest first. `q` is LIKE-escaped.
- Metadata: an undeclared key is `apperr.UnknownField` (400 `unknown_field`, path `metadata.<key>`) and wins over field errors; bad values are 422 on `metadata.<key>`. `null` values are dropped. `generic` accepts no keys.
- Prices are strings with exactly two decimals (no floats); at least one of min and max; max not below min; currency is 3 uppercase letters.
- `official_website_url` accepts only absolute http and https URLs.
- Image is read-only here: upload URLs are built as `/api/v1/attachments/{id}/file` and `/thumbnail`.
