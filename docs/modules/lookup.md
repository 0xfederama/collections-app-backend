# lookup

## Purpose

Merges values imported from a metadata provider (TMDB for movie and TV, Open Library for books, Hardcover as a book gap-filler) into items and records their provenance in `lookup_imports`. Provider adapters sit behind one `Provider` interface. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/lookup/service.go`, `provider.go`, `types.go`: `Service` (create, apply, owned refresh), `Provider`, `Mode`, `Store` contract
- `internal/lookup/tmdb_provider.go`, `openlibrary_provider.go`: adapters over the integration clients
- `internal/lookup/hardcover.go`, `bestmatch.go`: book gap-fill and automatic best match (see [hardcover](hardcover.md))
- `internal/lookup/apply.go`: `DBStore.Apply` and the create, existing and provenance writes
- `internal/lookup/repository.go`: `Repo` seam and `Postgres`, the only sqlc importer in the package
- `internal/lookup/store.go`: `DBStore`, `ListType`, `Read`, poster preparation
- `internal/lookup/merge.go`, `paths.go`: pure merge and path rules
- `internal/lookup/retention.go`: TMDB refresh and expiry (D-089)
- `internal/lookup/handler.go`, `wire.go`: Huma operations and their JSON types
- `db/queries/lookup_imports.sql`: provenance queries

## Interfaces

- `New(providers, store, itemSvc, settings, clk, workspaceID) *Service`; `(*Service).WithHardcover(BookGapFiller)`
- `(*Service).Create`, `Apply`, `RefreshOwned`
- `NewStore(db, fileStore, clk, items, log, workspaceID) *DBStore`; `DBStore` implements `Store` (`ListType`, `Read`, `Apply`)
- `(*DBStore).Maintain` and `Run`: TMDB retention pass and its loop
- `Register(api, log, svc, tmdb, books, settings)`: `listTmdbSuggestions`, `searchBookSuggestions`, `lookupItemMetadata`
- `POST /api/v1/items/{id}/lookup`: explicit fill, rematch or refresh with item `If-Match`

## Gotchas

- `lookup_imports` rows carry `provider` (`tmdb` or `openlibrary`) and `source_id`. Only TMDB has retention: refresh at five months, expiry at six. Open Library rows have NULL deadlines and maintenance never touches them (D-095).
- Attachment queries reach `apply` through `Repo` methods, so the merge and apply rules are testable with a fake repository.
- Poster files are written before the transaction and their rows inside it. Replaced files are deleted after commit.
- `FillOnly` values (book `stores: [preferred_store]`, D-096) apply only on create and fill, never overwrite chosen stores and are never tracked as imported.
- Automatic book lookup (no `source_id`) uses `pickBest`: exact normalised title first, then author and pages, ties in upstream order. TMDB takes the first result.
- Hardcover is used only by the automatic book path. `ModeCreateUntracked` creates without a provenance row or `openlibrary_id`. Failures are swallowed.
- `streaming.link` is an ordinary imported leaf. Clients never send it, and a streaming-country change clears it (see [workspace](workspace.md)) without bumping item `version`.
- User edits survive refresh and expiry, including on trashed items. Books keep the typed title on create; movies and TV adopt the provider title (kept in `imported_title`) and revert to the typed one on expiry if unchanged.
- A provider with no cover still applies the match, without an image. Manual lookup modes keep the existing image.
- TMDB backups must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
