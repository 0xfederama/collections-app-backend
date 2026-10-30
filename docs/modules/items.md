# items

## Purpose

Owns the items of a list: create, get, list, patch, move and reorder. The pure rules (metadata, price, patch, list query) have no database; `Service` runs them over a `Store`, and `Postgres` is the only SQL. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5, DATA_MODEL §2 and §7). Returns `apperr` errors; HTTP is Huma.

## Key files

- `internal/items/model.go`: `Item`, `Image`, `Price`, `CreateInput`, `Patch`/`Optional`, `ListParams`, `EscapeLike`
- `internal/items/metadata.go`: `ValidateMetadata`, `ValidateImport`, `MergePatch`, `PriceAllowed`
- `internal/items/validate.go`: field rules of create and patch (422), `ValidatePrice`
- `internal/items/listquery.go`: `Query`, filter, sort and paging of one list in Go
- `internal/items/service.go`: `Service` rules, `Store`/`Repo`/`TypeSource`/`SettingsSource`, preferred-store seed
- `internal/items/repository.go`, `price.go`: `Postgres`, one plain query per method, NUMERIC mapping
- `internal/items/handler.go`, `wire.go`: Huma operations and JSON shapes
- `internal/items/types/`: per-type metadata field rules; `db/queries/items.sql`: sqlc inputs

## Interfaces

- `NewService(store Store, types TypeSource, settings SettingsSource, clk clock.Clock) *Service`; `NewPostgres(db DB) *Postgres`
- `Service.Create(ctx, listID, CreateInput) (Item, error)`, `Get(ctx, id)`, `List(ctx, listID, ListParams) (ItemPage, error)`
- `Service.Update(ctx, id, version, Patch)`, `Move(ctx, id, version, dest)`, `Reorder(ctx, listID, ids) error`
- `Query(all, typ, ListDefs, ListParams)`: pure filter, sort and page
- `ValidateMetadata`, `ValidateImport`, `MergePatch`, `ValidatePrice`, `PriceAllowed`, `PriceParts`: rules used by lookup and clients
- `Register(api, log, svc, lk Importer)`: `GET`/`POST /api/v1/lists/{id}/items`, `GET`/`PATCH /api/v1/items/{id}`, `POST /api/v1/items/{id}/move`, `PUT /api/v1/lists/{id}/items/order`

## Gotchas

- Reads and writes see active rows only; a trashed or missing item or list is 404.
- Create, move and reorder take `LockTree` (D-060) in their transaction. Update does not, but it re-checks the item's list and type under the row lock.
- Type, preferred store and list info are read before the transaction. A type change in between is 409 `type_mismatch` on create and move, 412 on update.
- Update order: field rules (422), 404, 412, price (422), metadata (400 or 422). A patch that changes nothing writes nothing and keeps the version.
- Move order: 404, 412, 409 `destination_unavailable`, then 409 `type_mismatch`. Reorder: `ids` must equal the active items as a set (else 409 `order_stale`); no version bumps.
- List: all active items of the list are loaded in one query and filtered, sorted and paged in Go; `content_version` is read only for the returned page. Unknown sort or filter is 400 `invalid_parameter`.
- A list with no type (`type` null) has no definition: metadata must be `{}`, price is 422, only built-in sorts apply.
- `metadata` replaces the object and drops nulls. Undeclared keys are 400 `unknown_field`; invalid values are 422.
- `streaming.link` is server-managed: a client-sent link is dropped, and the stored one is kept while `streaming.country` is unchanged. Lookup imports (`ValidateImport`) may set it. Streaming filter and sort ignore it.
- `stores` fields are locked-only and not sortable or filterable. Create fills unset stores with `[preferred_store]`; an explicit value, `[]` included, wins.
- Price is NUMERIC(12,2) mapped to decimal strings, never floats; amount or min/max, never both, and only on types that allow it.
- Responses carry no computed link fields (API 0.17.0): the web builds store and platform links.
