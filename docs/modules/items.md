# items

## Purpose

Owns items of a list: create, get with path, paged list, partial update, move and reorder. An item takes the kind of its list (F-007); `items/kinds` checks its metadata. No HTTP; returns `apperr` errors. Delete, images, content and search live in their own modules. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2, D-062).

## Key files

- `internal/items/items.go`: `Service`, `Item` read model, `itemFromRow`, `Get`, `List`, `lockTree`
- `internal/items/write.go`: `Create`, `Update` (`mergePatch`), `Move`, `Reorder`, field rules
- `internal/items/price.go`: NUMERIC(12,2) to `"1299.00"` mapping (`big.Int`), `amount` and min/max rules
- `internal/items/kinds/kinds.go`: per-kind field registry, `Validate`, `SortKey`, `PinnableKeys`, `ValidatePinned`
- `db/queries/items.sql`: sqlc inputs; `GetItem` and `ListItems` share columns
- `internal/httpapi/items.go`: item handlers and body mapping
- `internal/items/price_test.go`, `kinds/kinds_test.go`, `internal/httpapi/items_test.go`: tests

## Interfaces

- `New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service`: `DB` is satisfied by `*pgxpool.Pool`
- `(*Service).Get(ctx, id) (Item, error)`: active item with `Path` (folders root first, then the list)
- `List(ctx, listID, ListParams) (ItemPage, error)`: limit 50 default, 200 max; `Sort` may be `price` or `kind_key`
- `Create(ctx, listID, CreateInput) (Item, error)`: appends last; kind is the list's kind
- `Update(ctx, id, version, Patch) (Item, error)`: `version` is the If-Match value
- `Move(ctx, id, version, dest) (Item, error)`: appends last in the destination list
- `Reorder(ctx, listID, ids) error`: `ids` must be the active items of the list
- `kinds.Validate(kind, map[string]any) (map[string]any, error)`: normalized metadata to store
- `kinds.Known(kind) bool`, `kinds.SortKey(kind) (key string, ok bool)`; kinds `Generic`, `Movie`, `TVShow`, `Motorcycle`, `Car`, `Camera`
- `kinds.PinnableKeys(kind) []string`, `kinds.ValidatePinned(kind, under, right)`: used by `tree` for list `pinned_fields`
- `FromSearchRow(sqlc.SearchItemsRow) Item`: maps a search row (used by `internal/search`)
- `EscapeLike(q) string`: LIKE-escapes `\`, `%` and `_` (exported for `internal/search`)

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) first. `Update` does not.
- Update order: field rules (422) run before the lookup; then 404, 412 `version_conflict`, then metadata (400 or 422). No real change returns the current version (no bump).
- `metadata` replaces the whole object. `null` values are dropped. `generic` accepts no keys.
- Move order: 404, 412, 409 `destination_unavailable`, then 409 `kind_mismatch` when the destination list has another kind.
- Reorder: `ids` must equal the active items as a set, else 409 `order_stale`; a missing list is 404; no version bumps.
- Sort: default is the list's `sort_mode`. `manual` uses `sort_order`, `title` is case-insensitive, `updated` is newest first. `price` orders by `price_min` ascending, no price last, then title; currencies are not converted. `kind_key` sorts by the kind's sort key (movie `release_year`, tv_show `first_air_year`, motorcycle `displacement_cc`, car `power_hp`, camera `megapixels`), empty last; `generic` has no sort key (400 `invalid_parameter`).
- Metadata: an undeclared key is `apperr.UnknownField` (400 `unknown_field`, path `metadata.<key>`) and wins over field errors; bad values are 422 on `metadata.<key>`.
- Motorcycle and camera have no `make`/`model` (migration 000003 moved them into a `## From 1.1` content block); car keeps them. Camera has `camera_type` (mirrorless, dslr, film); tv_show has `episodes_per_season` (1-500) and `episode_minutes` (1-1000).
- `streaming.paid_platforms` is optional; each entry must match a `platforms` entry (case-insensitive) and is stored in the platform's casing.
- Price: `amount` (one price, stored as min = max) or min/max, never both; two decimals, no floats; currency is 3 uppercase letters. A price on an item of a `generic` list is 422 on `price` (`null` is fine, F008-B1).
- `website` (was `official_website_url`) accepts only absolute http and https URLs; `summary` and `status` are gone (F-007, moved to content by 000002).
- Item summaries (list rows, search results, recent items) carry the item's `metadata` since F002-B5 (spec 0.5.0), so the web can show pinned fields.
- Image is read-only here (URLs `/api/v1/attachments/{id}/file` and `/thumbnail`).
