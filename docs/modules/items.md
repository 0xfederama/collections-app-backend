# items

## Purpose

Owns items of a list: create, get, page, patch, move and reorder. Metadata, price, sort and filters follow the list kind's database-backed definition. No HTTP; returns `apperr` errors.

## Key files

- `internal/items/items.go`: `Service`, `Item`, `Get`, `List`, `lockTree`
- `internal/items/write.go`: `Create`, `Update` (`mergePatch`), `Move`, `Reorder`, price-kind rule
- `internal/items/price.go`: NUMERIC(12,2) to `"1299.00"` mapping (`big.Int`)
- `internal/items/kinds/kinds.go`: pure type-specific metadata validators
- `db/queries/items.sql`, `internal/httpapi/items.go`: sqlc inputs; handlers and body mapping

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `Get(ctx, id) (Item, error)`: active item with `Path` (folders root first, then the list)
- `List(ctx, listID, ListParams) (ItemPage, error)`: field sort/filter plus manual, title, updated and allowed price sorts
- `Create(ctx, listID, CreateInput) (Item, error)`: appends last; kind is the list's kind
- `Update(ctx, id, version, Patch) (Item, error)`: `version` is the If-Match value
- `Move(ctx, id, version, dest) (Item, error)`: appends last in the destination list
- `Reorder(ctx, listID, ids) error`: `ids` must be the active items of the list
- `ValidateMetadata(ctx, q, kind, metadata) (map[string]any, error)`: definition-backed normalization
- `FromSearchRow(sqlc.SearchItemsRow) Item`, `EscapeLike(q) string`: used by `internal/search`

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) first. `Update` does not.
- Update order: field rules (422) first; then 404, 412 `version_conflict`, then metadata (400 or 422). No real change keeps the version.
- Move order: 404, 412, 409 `destination_unavailable`, then 409 `kind_mismatch`. Reorder: `ids` must equal the active items as a set, else 409 `order_stale`; no version bumps.
- `metadata` replaces the object and drops nulls. Undeclared keys are 400 `unknown_field`; invalid values are 422.
- Field filters: text/link/streaming use `filter_value`; boolean uses `true`/`false`; number/date use a min/max range.
- Price accepts amount or min/max, never both, and only when the definition allows it.
