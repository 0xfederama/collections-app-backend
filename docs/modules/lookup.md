# lookup

## Purpose

Merges values imported from a metadata provider (TMDB for movie and TV, Open Library for books, Hardcover as a book gap-filler) into items and records their provenance in `lookup_imports`. Movie and TV items are refreshed from TMDB once a week (D-123). Provider adapters sit behind one `Provider` interface. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/lookup/service.go`, `provider.go`, `types.go`: `Service` (create, apply, scheduled refresh), `Provider`, `Mode`, `Store` contract
- `internal/lookup/tmdb_provider.go`, `openlibrary_provider.go`: adapters over the integration clients
- `internal/lookup/hardcover.go`, `bestmatch.go`: book gap-fill and automatic best match (see [hardcover](hardcover.md))
- `internal/lookup/apply.go`: `DBStore.Apply` and the create, existing and provenance writes
- `internal/lookup/repository.go`: `Repo` seam and `Postgres`, the only sqlc importer in the package
- `internal/lookup/store.go`: `DBStore`, `ListType`, `Read`, poster preparation
- `internal/lookup/merge.go`, `paths.go`: pure merge and path rules
- `internal/lookup/maintenance.go`: the weekly TMDB refresh pass, its retry state and its loop
- `internal/lookup/handler.go`, `wire.go`: Huma operations and their JSON types
- `db/queries/lookup_imports.sql`: provenance and scheduled-item queries

## Interfaces

- `New(providers, store, itemSvc, settings, clk, workspaceID) *Service`; `(*Service).WithHardcover(BookGapFiller)`
- `(*Service).Create`, `Apply` (fill or rematch), `RefreshScheduled` (the weekly refresh of one item)
- `NewStore(db, fileStore, clk, items, log, workspaceID) *DBStore`; `DBStore` implements `Store` (`ListType`, `Read`, `Apply`)
- `(*DBStore).Maintain` and `Run`; `MaintenanceInterval` (one hour) is the tick. `cmd/api` runs one pass at startup
- `Register(api, log, svc, tmdb, books, settings)`: `listTmdbSuggestions`, `searchBookSuggestions`, `lookupItemMetadata`
- `POST /api/v1/items/{id}/lookup`: explicit fill or rematch with item `If-Match`

## Gotchas

- Weekly refresh (D-123): a movie or TV item with a numeric `tmdb_id`, not trashed and not `lookup_disabled`, is due 7 days after its last fetch (`lookup_imports.fetched_at`). A typed id with no provenance is due at once. A pass refreshes at most 10 items, oldest first. Each refresh overwrites every fetched field and the title, user edits included; a field TMDB no longer returns is removed. A user image is kept; a poster is replaced only when it is the one the import fetched and TMDB's path changed.
- A failed refresh writes nothing. A 429 stops the pass (retried next pass); a TMDB 404 waits a week; any other failure (network, 5xx, bad body) waits a day. Retry times are in memory, so a restart retries once. Disabled lookup stops the pass.
- Books are never refreshed (D-095, D-123). Open Library rows have NULL deadlines.
- TMDB rows still get `refresh_after`, `expires_at` and the poster deadlines (five and six months after the fetch) because `lookup_imports_retention_check` requires them. Nothing in this release reads them. An older release on the same database (rollback) does read them and would expire TMDB values the weekly refresh no longer touches (`lookup_disabled`, trashed). A later contract migration drops them.
- Attachment queries reach `apply` through `Repo` methods, so the merge and apply rules are testable with a fake repository.
- Poster files are written before the transaction and their rows inside it. Replaced files are deleted after commit.
- `FillOnly` values (book `stores: [preferred_store]`, D-096) apply only on create and fill, never overwrite chosen stores and are never tracked as imported.
- `rematch` replaces the fetched fields, user edits to them included. `fill` never replaces a non-empty value. `refresh` is not a mode any more (D-123).
- Automatic book lookup (no `source_id`) uses `pickBest`: exact normalised title first, then author and pages, ties in upstream order. TMDB takes the first result.
- Hardcover is used only by the automatic book path. `ModeCreateUntracked` creates without a provenance row or `openlibrary_id`. Failures are swallowed.
- `streaming.link` is an ordinary imported leaf. Clients never send it, and a streaming-country change clears it (see [workspace](workspace.md)) without bumping item `version`.
- Books keep the typed title on create; movies and TV adopt the provider title (kept in `imported_title`) on create and rematch.
- A provider with no cover still applies the match, without an image. Manual lookup modes keep the existing image.
- TMDB backups must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
