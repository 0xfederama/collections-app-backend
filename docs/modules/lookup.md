# lookup

## Purpose

Merges values imported from a metadata provider (TMDB for movie and TV, Open Library for books) into items and records their provenance in `lookup_imports`. Provider adapters are behind one `Provider` interface; design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/lookup/provider.go`: `Provider` interface, provider registry types, source-id validation, error mapping
- `internal/lookup/tmdb_provider.go`, `internal/lookup/openlibrary_provider.go`: adapters over the TMDB and Open Library clients
- `internal/lookup/service.go`: create, apply (fill/rematch/refresh), owned refresh
- `internal/lookup/hardcover.go`: optional Hardcover gap-filler for the automatic book lookup (see [hardcover](hardcover.md))
- `internal/lookup/store.go`, `internal/lookup/types.go`: provenance read/apply and the `Store` contract
- `internal/lookup/maintenance.go`: TMDB-only refresh and expiry pass
- `db/queries/lookup_imports.sql`, `db/migrations/000022_lookup_imports.up.sql`: provenance table and queries

## Interfaces

- `Provider`: `Name`, `Enabled`, `ValidateSourceID`, `Search`, `Details`, `DownloadImage`
- `Mode`: `create`, `fill`, `rematch`, `refresh`, plus automatic owned refresh
- `Store.ListType`, `Store.Read`, `Store.Apply`: list type check, provenance read, atomic item/image/provenance write
- `GET /api/v1/tmdb/suggestions`, `GET /api/v1/books/suggestions`: provider search (see [tmdb](tmdb.md), [openlibrary](openlibrary.md))
- `POST /api/v1/items/{id}/lookup`: explicit fill/rematch/refresh with item `If-Match`; `source_id` selects a match
- Item create may request lookup (`tmdb_id` or `source_id`); a disabled provider creates the typed original item

## Gotchas

- `lookup_imports` rows carry `provider` (`tmdb` or `openlibrary`) and `source_id`; `tmdb_imports` is gone (migration 000022).
- Only TMDB has retention: refresh at five months, expiry at six (D-089). Open Library rows have no deadlines and maintenance never touches them (D-095).
- `FillOnly` values (for books, `stores: [preferred_store]` added by the service via `defaultStoreFill`, D-096) are applied only on create and fill (absent, null or, on fill, empty), never overwrite user-chosen stores and are never tracked as imported. Providers no longer put an Open Library link in `stores`.
- Automatic book lookup (no `source_id`) does not blindly take the first Open Library result: `pickBest` (`bestmatch.go`) keeps only results whose normalised title (lowercase, no accents or punctuation) equals the query, prefers the one with both author and pages (then either), ties to upstream order; no exact title (or an ISBN query) means the first result. TMDB is unchanged.
- Automatic book lookup may use Hardcover (`Service.WithHardcover`): fill-only `author`/`pages` by ISBN when the chosen Open Library work lacks them, or, when Open Library has no result, an exact-title Hardcover match applied with `ModeCreateUntracked` (no `lookup_imports` row, no `openlibrary_id`). Explicit requests never use it; failures are swallowed.
- User edits survive refresh and expiry, including on trashed items. If an unchanged imported title expires, the original typed title is restored.
- Poster expiry is tracked separately from metadata refresh; manual lookup modes keep the existing image.
- A provider with no cover (Open Library) still applies the match, without an image (`ErrNoImage`).
- TMDB backups must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
- Books keep the title the user typed on create (also with an explicit pick or `source_id`); only movies and TV adopt the provider title. The provider title stays in `imported_title`, so a later refresh does not rename the item.
