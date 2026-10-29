# Docs index

- [api-contract](modules/api-contract.md): OpenAPI contract and generated Go server stubs
- [apperr](modules/apperr.md): typed domain errors, no HTTP imports
- [backup](modules/backup.md): Postgres plus images backup, restore and verify scripts (`scripts/`)
- [config](modules/config.md): env var parsing and startup validation
- [content](modules/content.md): item Markdown content, versions, restore and retention
- [database](modules/database.md): pgx pool, schema migrations, sqlc codegen
- [definitions](modules/definitions.md): workspace type and field definitions
- [httpapi](modules/httpapi.md): legacy HTTP router, probes, tree/folder/list/item/content/type handlers, book suggestions and store providers
- [images](modules/images.md): item image upload/URL/delete, metadata stripping on upload, validation, thumbnails, serving, orphan sweeper
- [streamlinks](modules/streamlinks.md): streaming service search URLs (`streaming.platform_links`)
- [items](modules/items.md): items CRUD, move and reorder, per-type metadata validation (`items/types`), read-only `store_links`
- [lookup](modules/lookup.md): metadata provider lookup (TMDB, Open Library) merge rules and imported-field provenance/retention
- [openlibrary](modules/openlibrary.md): bounded Open Library client for book search, work details and covers
- [platform](modules/platform.md): shared infrastructure (Huma on chi, problem+json, scope, etag, clock, testdb, layer archtest)
- [seed](modules/seed.md): idempotent Go seed of the five built-in types and their locked fields, run at every bootstrap
- [search](modules/search.md): item title search with folder and list paths (`GET /api/v1/search`) and recent items (`GET /api/v1/recent-items`)
- [smoke](modules/smoke.md): HTTP smoke test over a running stack (`scripts/smoke.sh`, `make smoke`)
- [stores](modules/stores.md): fixed online store list and read-time store link builder for `stores` fields
- [storage](modules/storage.md): local file store under DATA_DIR (`os.Root`, generated keys)
- [trash](modules/trash.md): delete into trash batches, trash list, restore, purge, empty trash, retention auto-purge
- [tree](modules/tree.md): folders/lists CRUD, move/reorder, sidebar tree, list icons and director sort mode
- [tmdb](modules/tmdb.md): bounded server-side TMDB client for movie and TV suggestions/details
- [workspace](modules/workspace.md): single-workspace bootstrap and settings service (Huma `GET`/`PATCH /api/v1/settings`)

Design docs (canonical): `/Users/federico/Developer/collections-app/design-docs/`

TMDB setup: get an API key through [TMDB account API settings](https://www.themoviedb.org/settings/api) and set `TMDB_API_KEY` in the server's `deploy/server/.env`; never place the key in browser configuration. The product must display an [approved TMDB logo and attribution](https://www.themoviedb.org/about/logos-attribution) in About. [TMDB watch-provider data](https://developer.themoviedb.org/reference/tv-series-watch-providers) requires JustWatch attribution. TMDB-imported metadata and posters expire within six months; user edits are preserved. Backups containing TMDB data must also be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
