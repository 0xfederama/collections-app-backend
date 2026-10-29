# items

## Purpose

Owns items of a list: create, get, page, patch, move and reorder. Metadata, price, sort and filters follow the list type's database-backed definition. No HTTP; returns `apperr` errors.

## Key files

- `internal/items/items.go`: `Service`, `Item`, `Get`, `List`, `lockTree`
- `internal/items/write.go`: `Create`, `Update` (`mergePatch`), `Move`, `Reorder`, price-type rule
- `internal/items/stores.go`: default store on create (`applyPreferredStores`)
- `internal/items/price.go`: NUMERIC(12,2) to `"1299.00"` mapping (`big.Int`)
- `internal/items/types/types.go`: pure type-specific metadata validators
- `db/queries/items.sql`, `internal/httpapi/items.go`: sqlc inputs; handlers and body mapping

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `Get(ctx, id) (Item, error)`: active item with `Path` (folders root first, then the list)
- `List(ctx, listID, ListParams) (ItemPage, error)`: field sort/filter plus manual, title, updated and allowed price sorts
- `Create(ctx, listID, CreateInput) (Item, error)`: appends last; type is the list's type
- `Update(ctx, id, version, Patch) (Item, error)`: `version` is the If-Match value
- `Move(ctx, id, version, dest) (Item, error)`: appends last in the destination list
- `Reorder(ctx, listID, ids) error`: `ids` must be the active items of the list
- `ValidateMetadata(ctx, q, type *string, metadata) (map[string]any, error)`: definition-backed normalization
- `FromSearchRow(sqlc.SearchItemsRow) Item`, `EscapeLike(q) string`: used by `internal/search`

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) first. `Update` does not.
- Update order: field rules (422) first; then 404, 412 `version_conflict`, then metadata (400 or 422). No real change keeps the version.
- Move order: 404, 412, 409 `destination_unavailable`, then 409 `type_mismatch`. Reorder: `ids` must equal the active items as a set, else 409 `order_stale`; no version bumps.
- `metadata` replaces the object and drops nulls. Undeclared keys are 400 `unknown_field`; invalid values are 422. Responses carry no computed link fields (API 0.17.0): the web builds store and platform links.
- `streaming.link` (JustWatch page, set by the TMDB lookup) is server-managed: `validateClientMetadata` drops a client-sent link on create/PATCH and keeps the stored one while `streaming.country` is unchanged, so editing only the platforms or round-tripping the object keeps it. `ValidateMetadata` (lookup path) accepts it. Streaming filter/sort ignore the link.
- `stores` fields are locked-only, hold provider keys, and are neither sortable nor filterable (400). Create fills unset (absent or null) stores fields with `[preferred_store]`; an explicit value, `[]` included, wins. `metadata.openlibrary_id` on books is lookup-owned, read-only by convention (clients build the Open Library link from it).
- Field filters: text/link/streaming use `filter_value`; boolean uses `true`/`false`; number/date use a min/max range.
- Price accepts amount or min/max, never both, and only when the definition allows it (among the built-ins only `wishlist`).
- A list with no type (`Item.Type == nil`, API `type: null`) has no definition: metadata must be `{}`, price is 422, only built-in sorts apply, field sorts/filters are `invalid_parameter`; moves between no-type lists are fine, no-type vs typed is `type_mismatch`.
