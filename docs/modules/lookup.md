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
- `internal/lookup/backfill.go`: one-time `streaming.link` backfill for items looked up before api 0.14.0
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
- `streaming.link` is an ordinary imported leaf (snapshot path `streaming.link`): refreshed with the other TMDB values, removed by expiry while unedited, never taken from a client create input. Changing the workspace streaming country clears it from `items.metadata` and from the snapshots in the settings transaction (see [workspace](workspace.md)); it is derived data, so item `version`/`updated_at` do not change.
- `streaming.link` backfill (`DBStore.RunStreamingLinkBackfill`, started as a goroutine from `cmd/api` after the startup maintenance, never blocks start-up, recovers panics, stops with the shutdown context). Per pass, at most 500 TMDB calls (`/{movie|tv}/{id}/watch/providers`, one each, paced by the client at 4 calls/s), keyset-paged. Candidates: active TMDB movie/TV items whose `metadata.streaming.country` is the workspace country, with non-empty `platforms`, no `link` key, no `streaming.link` entry in `imported_fields`, `tmdb_id` equal to the import's `source_id`, and `lookup_disabled` not true. Trashed items are skipped (as refresh does) and are picked up after a restore. It writes only `metadata.streaming.link` and `imported_fields['streaming.link']` (under the tree and item locks, rechecked), with no `version`/`updated_at` bump (derived data, like the country-change clear; recent-items order and ETags stay) and no change to `fetched_at`/deadlines. "No link for the country" and a 404 are recorded as the snapshot value `none:<COUNTRY>` (no item change) so the item is not asked again; any later import write drops it (the snapshot path is absent from the item) and a streaming-country change clears it with the other link snapshots. Transient errors leave the item unmarked (retried next startup; five in a row end the pass), a 429 or a disabled client ends the pass. One INFO line `streaming link backfill` (scanned, updated, no_link, skipped, errors) when anything was scanned; nothing to do or no TMDB key/country logs at DEBUG.
- The regular owned refresh needs no extra code to fill a missing link: it re-reads `watch/providers`, and a candidate leaf the item lacks is set and snapshotted (covered by `TestBackfill_RegularRefreshFillsAMissingLinkAndDropsTheMarker`). It only runs after five months, which is why the backfill exists.
- User edits survive refresh and expiry, including on trashed items. If an unchanged imported title expires, the original typed title is restored.
- Poster expiry is tracked separately from metadata refresh; manual lookup modes keep the existing image.
- A provider with no cover (Open Library) still applies the match, without an image (`ErrNoImage`).
- TMDB backups must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
- Books keep the title the user typed on create (also with an explicit pick or `source_id`); only movies and TV adopt the provider title. The provider title stays in `imported_title`, so a later refresh does not rename the item.
