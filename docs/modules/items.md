# items

## Purpose

Owns items of a list: create, get with path, paged list, partial update, move and reorder. An item takes its list's kind (F-007); `items/kinds` checks metadata. No HTTP; returns `apperr` errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2, D-062).

## Key files

- `internal/items/items.go`: `Service`, `Item`, `Get`, `List`, `lockTree`
- `internal/items/write.go`: `Create`, `Update` (`mergePatch`), `Move`, `Reorder`, price-kind rule
- `internal/items/price.go`: NUMERIC(12,2) to `"1299.00"` mapping (`big.Int`)
- `internal/items/kinds/kinds.go`: kind registry, metadata validation, sort and pin rules
- `db/queries/items.sql`, `internal/httpapi/items.go`: sqlc inputs; handlers and body mapping

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `Get(ctx, id) (Item, error)`: active item with `Path` (folders root first, then the list)
- `List(ctx, listID, ListParams) (ItemPage, error)`: limit 50 default, 200 max; movie lists can filter by director and sort by director
- `Create(ctx, listID, CreateInput) (Item, error)`: appends last; kind is the list's kind
- `Update(ctx, id, version, Patch) (Item, error)`: `version` is the If-Match value
- `Move(ctx, id, version, dest) (Item, error)`: appends last in the destination list
- `Reorder(ctx, listID, ids) error`: `ids` must be the active items of the list
- `kinds.Validate(kind, map[string]any) (map[string]any, error)`: normalized metadata to store
- `kinds.Known`, `kinds.SortKey`, `kinds.HasPrice`, `kinds.PinnableKeys`, `kinds.ValidatePinned`
- `FromSearchRow(sqlc.SearchItemsRow) Item`, `EscapeLike(q) string`: used by `internal/search`

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) first. `Update` does not.
- Update order: field rules (422) first; then 404, 412 `version_conflict`, then metadata (400 or 422). No real change keeps the version.
- Move order: 404, 412, 409 `destination_unavailable`, then 409 `kind_mismatch`. Reorder: `ids` must equal the active items as a set, else 409 `order_stale`; no version bumps.
- `metadata` replaces the whole object; `null` values are dropped; `generic` accepts no keys. Undeclared key: 400 `unknown_field` (`metadata.<key>`); bad value: 422 on `metadata.<key>`.
- Price: `amount` (min = max) or min/max, never both; two decimals, no floats. Price exists only on `motorcycle`, `car` and `camera` items (F009-B1, subsuming F008-B1): a non-null price on a `generic`, `movie` or `tv_show` item is 422 on `price` (`null` is fine).
- Sort: default is the list's `sort_mode`. `price` orders by `price_min`, no price last, then title; `?sort=price` on a kind without a price (generic, movie, tv_show) is 400 `invalid_parameter`, like `kind_key` on `generic`.
- Motorcycle and camera have no `make`/`model` (000003 moved them to a `## From 1.1` content block).
- Item summaries carry `metadata` (F002-B5); `image` is read-only here.
- Movie `director` is text up to 300 characters and may be pinned; `tmdb_id` is provider identity and `lookup_disabled` opts an item out of automatic lookup. Provider bookkeeping fields cannot be pinned.
