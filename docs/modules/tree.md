# tree

## Purpose

Serves `GET /api/v1/tree`: every active folder and list, flat, for the client to build the sidebar. It is a handler-only read feature: no service, no data, no writes. Folders and lists come from their own packages through consumer interfaces. Design context: `/Users/federico/Developer/collections-app/design-docs/` (API.md §5).

## Key files

- `internal/tree/handler.go`: `Register`, `getTree`, `Tree`/`TreeOutput`, `folderLister`/`listLister`
- `internal/folders/wire.go`, `internal/lists/wire.go`: element shapes (`FolderJSON`, `ListJSON`)

## Interfaces

- `Register(api huma.API, log *slog.Logger, folders folderLister, lists listLister)`: called from `internal/httpapi/router.go`
- `folderLister` / `listLister`: `ListActive(ctx)`; satisfied by `*folders.Postgres` and `*lists.Postgres`
- `GET /api/v1/tree` (`getTree`): 200 `Tree` = `{ folders: [...], lists: [...] }`, ordered by `(sort_order, id)`

## Gotchas

- Only active rows: trashed folders and lists are absent.
- Both arrays are always present, empty rather than `null`.
- Folders and lists are read by two separate queries with no transaction, so a write between them can show in one array and not the other.
- Lists in the response have no `path`; use `GET /api/v1/lists/{id}` for it.
- The router passes the same repositories to `folders` and `lists`; nothing here takes the tree lock.
