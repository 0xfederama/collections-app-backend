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
- `internal/lookup/validate_import.go`: `validateImport`, which drops invalid provider fields instead of failing the write
- `internal/lookup/maintenance.go`: the weekly TMDB refresh pass, its retry state and its loop
- `internal/lookup/backfill.go`: the generic backfill pass (D-164): `DBStore.Backfill`, its ticker `RunBackfill`, and the book step `Service.BackfillFill`
- `internal/lookup/description.go`: the provider summary as the item's description: `normalizeSynopsis`, the tracking rules of `writeDescription`, `currentImportGeneration`
- `internal/lookup/refresh.go`: `RefreshScheduled` (the one-item TMDB refresh both paths share), the explicit `refresh` mode and `RefreshList` (the list refresh)
- `internal/lookup/handler.go`, `wire.go`: Huma operations and their JSON types
- `db/queries/lookup_imports.sql`: provenance and scheduled-item queries

## Interfaces

- `New(providers, store, itemSvc, settings, clk, workspaceID) *Service`; `(*Service).WithHardcover(BookGapFiller)`
- `(*Service).Create`, `Apply` (fill, rematch or refresh), `RefreshScheduled` (the weekly refresh of one item), `RefreshList` (the refresh of a list)
- `NewStore(db, fileStore, clk, items, log, workspaceID) *DBStore`; `DBStore` implements `Store` (`ListType`, `Read`, `Apply`)
- `(*DBStore).Maintain` and `Run`; `MaintenanceInterval` (one hour) is the tick. `cmd/api` runs one pass at startup
- `(*DBStore).Backfill(ctx, tmdb, openLibrary RefreshFunc)` and `RunBackfill`; `BackfillInterval` (ten minutes) is its tick, with no startup pass. `(*Service).BackfillFill` is the book step
- `Register(api, log, svc, tmdb, books, settings)`: `listTmdbSuggestions`, `searchBookSuggestions`, `lookupItemMetadata`
- `POST /api/v1/items/{id}/lookup`: explicit fill, rematch or refresh with item `If-Match`
- `POST /api/v1/lists/{id}/lookup-refresh` (`refreshListLookups`, tag `lookup`): the list refresh, no `If-Match`; answers `{updated, skipped, failed, remaining}`

## Gotchas

- Weekly refresh (D-123): a movie or TV item with a numeric `tmdb_id`, not trashed and not `lookup_disabled`, is due 7 days after its last fetch (`lookup_imports.fetched_at`). A typed id with no provenance is due at once. A pass refreshes at most 10 items, oldest first. Each refresh overwrites every fetched field and the title, user edits included; a field TMDB no longer returns is removed. A user image is kept; a poster is replaced only when it is the one the import fetched and TMDB's path changed.
- A failed refresh writes nothing. A 429 stops the pass (retried next pass); a TMDB 404 waits a week; any other failure (network, 5xx, bad body) waits a day. Retry times are in memory, so a restart retries once. Disabled lookup stops the pass.
- Books are never refreshed (D-095, D-123). Open Library rows have NULL deadlines.
- TMDB rows still get `refresh_after`, `expires_at` and the poster deadlines (five and six months after the fetch) because `lookup_imports_retention_check` requires them. Nothing in this release reads them. An older release on the same database (rollback) does read them and would expire TMDB values the weekly refresh no longer touches (`lookup_disabled`, trashed). A later contract migration drops them.
- Attachment queries reach `apply` through `Repo` methods, so the merge and apply rules are testable with a fake repository.
- Summary (F-028, D-162): `Details.Synopsis` (normalized: one paragraph, leading Markdown escaped, at most 5,000 characters) is written inside `Apply`'s transaction as an `item_content_versions` row with `origin = 'lookup'`, pruned to `ApplyParams.HistoryLimit` (read before the transaction). It fills an item with no content version (version 0) in every mode. A user-saved body, even `""`, is never filled. While the text is still the lookup's (`lookup_imports.description_version` and `description_sha256` match the current version), rematch and refresh replace it when it changed; fill leaves it. A user save makes a new version, which clears the tracking. A missing summary changes nothing. An untracked Hardcover create writes origin NULL with the caller as author.
- Lookup-owned fields (F-028, D-162): `tmdb_rating`, `tmdb_vote_count` and `genres` are written only by the lookup. The item rules (`items.ValidateMetadata`) refuse a client value that differs from the stored one with 422, keep the stored value when the client omits the key, and accept an equal value. `RejectLookupOwned` refuses them on a lookup create.
- TMDB details (F-028 B1): `overview` (English fallback: one extra `en-US` details call only when the requested language is not English and the overview is empty), `vote_average` rounded to one decimal only from ten votes or more, `vote_count` always, and `genres` (at most ten names of at most 100 characters).
- Open Library work description (F-028 B2): one extra request to `/works/{id}.json` per book lookup, best effort. A 429 maps to `ErrProviderRateLimited`.
- Backfill (F-028 B4, D-164): a pass re-reads at most 25 items whose `lookup_imports.import_generation` is below `currentImportGeneration`, oldest fetch first. A movie or TV item uses the weekly refresh rule; a book uses `BackfillFill` (fill only). Trashed and `lookup_disabled` items are skipped and stay below, so they do not block completion. A rate limit or disabled lookup stops the pass. Failures wait in memory for the same retry intervals (a separate map, since the pass runs on its own goroutine). Logs: `lookup backfill progress` (done, remaining, failed, stopped) every pass and `lookup backfill complete` once per process. Bump `currentImportGeneration` to re-read every provider-linked item again.
- Who changed: an explicit lookup create or apply records the request's user on the item. The weekly refresh runs with no user, so it keeps the item's `updated_by_user_id`.
- Poster files are written before the transaction and their rows inside it. Replaced files are deleted after commit.
- `FillOnly` values (book `stores: [preferred_store]`, D-096) apply only on create and fill, never overwrite chosen stores and are never tracked as imported.
- `rematch` replaces the fetched fields, user edits to them included. `fill` never replaces a non-empty value.
- `refresh` (D-123) is the weekly rule on demand, for movie and TV only (a book answers 422). It runs on a `lookup_disabled` item too (the user asked). The id is the stored `tmdb_id`, or the one selected. A provider failure answers the lookup errors (`ProviderError`): 429, 404 and 5xx all answer 503 `upstream_unavailable`, a disabled key 503 `lookup_disabled`; nothing is written.
- The list refresh (`RefreshList`) takes at most 50 items per call (`listRefreshBatchSize`: about 25 s of TMDB pacing, inside the 60 s write timeout). The oldest fetches go first, so a call again reaches the rest; eligible items beyond the batch are `remaining` (not `skipped`), and the client runs again until it is 0. Books, untyped lists, items without a TMDB id and `lookup_disabled` items are `skipped`; an item changed during the run is `skipped` (its version moved). A 429, a disabled key or a fetch failure other than 404 stops the call, and the eligible items of the batch not yet reached count as `failed` (items beyond the batch stay in `remaining`). A 404 for one item is `failed` and the run goes on. A cancelled request returns its context error; the items already refreshed stay refreshed. No `If-Match`: each item is checked against the version the list read.
- The list refresh is wired through `Store.ListItems` (the `ListLookupItems` query, the items of one list in refresh order), so the lists feature is not involved.
- Automatic book lookup (no `source_id`) uses `pickBest`: exact normalised title first, then author and pages, ties in upstream order. TMDB takes the first result.
- Hardcover is used only by the automatic book path. `ModeCreateUntracked` creates without a provenance row or `openlibrary_id`. Failures are swallowed.
- `streaming.link` is an ordinary imported leaf. Clients never send it, and a streaming-country change clears it (see [workspace](workspace.md)) without bumping item `version`.
- Provider data never blocks create or refresh (R28): a top-level metadata key that fails the item rules is dropped and logged (`lookup: dropped invalid provider fields`). On an existing item the stored value takes its place when there is one. On create, keys the client sent and the provider id are never dropped; their errors still answer 422.
- Books keep the typed title on create; movies and TV adopt the provider title (kept in `imported_title`) on create and rematch.
- A provider with no cover still applies the match, without an image. Manual lookup modes keep the existing image.
- TMDB backups must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
