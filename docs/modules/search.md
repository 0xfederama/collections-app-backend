# search

## Purpose

Finds active items by title and returns each match with its folder/list path and list type name. Also lists the most recently updated items. Read-only; Huma operations; returns typed errors. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5).

## Key files

- `internal/search/service.go`: `Service`, `Search`, `Recent`, path building from folders and lists (`tree`, `of`)
- `internal/search/model.go`: limits, `Params`, `Result`, `Page`, `Folder`, `List`
- `internal/search/repository.go`: `Postgres` plain reads, `itemOf` row mapper
- `internal/search/handler.go`, `wire.go`: `searchItems` and `listRecentItems` Huma operations
- `db/queries/search.sql`: `SearchItems`, `CountSearchItems`, `ListRecentItems`, `ListSearchFolders`, `ListSearchLists`
- `internal/httpapi/search_test.go`, `recent_test.go`: HTTP tests (`TestB12_*` for recent items)

## Interfaces

- `NewService(store Store) *Service`; `NewPostgres(db sqlc.DBTX) *Postgres` implements `Store`
- `(*Service).Search(ctx, Params{Q, Limit, Offset}) (Page, error)`: limit default 50, max 200
- `Page{Results, Limit, Offset, Total}`; `Result{Item, TypeName, Path}` (`TypeName` nil for a list with no type)
- `(*Service).Recent(ctx, limit) ([]Result, error)`: limit default 12, max 50, newest `updated_at` first
- `WithAccess(a Access) *Service`: `Access` is `VisibleListIDs` (see [access](access.md)); `Search` and `Recent` filter on it
- `ErrInvalidQuery`: `q` is empty after trimming or over 200 characters
- `Register(api, log, svc)`: `GET /api/v1/search` (200 page), `GET /api/v1/recent-items` (200 `{data}`, not paginated)

## Gotchas

- Matches titles only. `%`, `_` and `\` are escaped with `items.EscapeLike`.
- Order: exact case-insensitive match first, then `lower(title)`, then id.
- Only active items, and only in lists the caller may see (`VisibleListIDs`). Batch cascade (D-004) trashes children with their list or folder, so there is no ancestor check.
- Without `WithAccess` nothing is visible: results are empty, never every user's items.
- Paths and type names come from all active folders and lists, loaded once per call that has rows. Root folder first, list last.
- A row whose list vanished between reads is skipped, so a page can hold fewer rows than `limit`. `Total` still counts it (separate count).
- Whitespace-only `q` is caught by the service (`ErrInvalidQuery` becomes 400 `invalid_parameter`); the schema rejects empty or over-long raw `q`.
- A present but empty `limit` or `offset` is 400: Huma reads an empty value as absent, so `Resolve` rejects it.
- Trigram index `items_title_trgm_idx` helps only 3+ character patterns on large tables; a seq scan on small tables is expected.
- A PATCH or content save moves an item up in `Recent` (both bump `updated_at`).
