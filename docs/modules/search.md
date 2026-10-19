# search

## Purpose

Finds active items by title and returns their folder/list path and list type name. Also lists most recently updated items. Read-only; no HTTP, returns typed errors.

## Key files

- `internal/search/search.go`: `Service`, `Search`, path building from folders and lists (`tree`, `of`)
- `db/queries/search.sql`: `SearchItems`, `CountSearchItems`, `ListRecentItems`, `ListSearchFolders`, `ListSearchLists`
- `internal/httpapi/search.go`: `SearchItems` and `ListRecentItems` handlers, 400 mapping, path body
- `internal/httpapi/search_test.go`: `TestAT12_*` tests; `recent_test.go`: `TestB12_*` tests

## Interfaces

- `New(db sqlc.DBTX, workspaceID uuid.UUID) *Service`
- `(*Service).Search(ctx, Params{Q, Limit, Offset}) (Page, error)`: limit default 50, max 200
- `Page{Results []Result, Limit, Offset, Total}`; `Result{Item, TypeName, Path}` (`TypeName` is nil for a list with no type)
- `(*Service).Recent(ctx, limit) ([]Result, error)`: limit default 12, max 50, newest `updated_at` first, then id
- `GET /api/v1/recent-items?limit=`: 200 `{ data: [SearchResult] }`, no `page`; 400 `invalid_parameter` for a limit outside 1–50 (the guard answers)
- `ErrInvalidQuery`: `q` is empty after trimming or over 200 characters
- `GET /api/v1/search?q=&limit=&offset=`: 200 page; 400 `invalid_parameter` for blank `q`

## Gotchas

- Matches titles only. `%`, `_` and `\` are escaped in Go with `items.EscapeLike`.
- Order: exact title match (case-insensitive) first, then `lower(title)`, then id.
- Only active items. Batch cascade (D-004) trashes children with their list or folder, so there is no ancestor check and no join with lists.
- Paths and list type names come from all active folders and lists loaded once per request. Root folder is first; list is last.
- A row whose list vanished between reads is skipped, so a page can hold fewer rows than `limit`. `Total` still counts it.
- `Total` comes from a separate count, so it stays right past the last page.
- The request guard rejects an empty or too long raw `q`; whitespace-only `q` is caught by the service.
- Trigram index `items_title_trgm_idx`: Postgres uses it only for patterns with 3+ useful characters on large tables. At about 5k rows a seq scan (~3 ms) wins; an index scan appears near 55k rows for rare terms.
- `Recent` is read-only and reuses the search read model and path building. A PATCH or a content save moves an item up (both bump `updated_at`). No index: a seq scan plus top-N sort took about 4 ms at 20k items.
