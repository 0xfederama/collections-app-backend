# Docs index

- [api-contract](modules/api-contract.md): OpenAPI 3.1 contract generated from the Huma operations (`make spec`, `make check-spec`)
- [api-notes](api-notes.md): what the generated OpenAPI contract leaves out (request constraints kept in services, error statuses the server does not return as listed)
- [apperr](modules/apperr.md): typed domain errors, no HTTP imports
- [architecture](architecture.md): layer map, package rules, tree lock and file ordering, how to add an endpoint
- [backup](modules/backup.md): Postgres plus images backup, restore and verify scripts (`scripts/`)
- [config](modules/config.md): env var parsing and startup validation
- [content](modules/content.md): item Markdown content, versions, restore and retention
- [database](modules/database.md): pgx pool, schema migrations, sqlc codegen
- [definitions](modules/definitions.md): `GET /api/v1/types`, the built-in types and fields served from the Go registry; no custom fields, no type writes
- [folders](modules/folders.md): folder create, get, rename, move (cycle check) and reorder (Huma)
- [httpapi](modules/httpapi.md): HTTP composition root: router build, Huma registration of every feature package, health probes, `Spec()`
- [images](modules/images.md): item image upload/URL/delete, metadata stripping on upload, validation, thumbnails, serving, orphan sweeper
- [items](modules/items.md): items create, get, list, patch, move and reorder (Huma); pure metadata, price and list-query rules; per-type rules in `items/types`
- [lists](modules/lists.md): list create, get with path, update (type change, pure patch planner), move and reorder (Huma)
- [logs](modules/logs.md): in-memory ring of the newest log records and the `LOGS_UI` viewer (`GET /admin/logs`, `GET /admin/logs/view`, hidden from the spec)
- [lookup](modules/lookup.md): metadata provider lookup (TMDB, Open Library, Hardcover gap-fill): merge rules, provenance and the weekly TMDB refresh (Huma)
- [openlibrary](modules/openlibrary.md): bounded Open Library client for book search, work details and covers (`internal/integrations`)
- [hardcover](modules/hardcover.md): optional bounded Hardcover client, book gap-filler for automatic lookup (`internal/integrations`)
- [platform](modules/platform.md): shared infrastructure (Huma on chi, problem+json and its code list, scope, etag, clock, shared wire types, testdb, layer archtest)
- [search](modules/search.md): item title search with folder and list paths and recent items (Huma: `GET /api/v1/search`, `GET /api/v1/recent-items`)
- [smoke](modules/smoke.md): HTTP smoke test over a running stack (`scripts/smoke.sh`, `make smoke`)
- [stores](modules/stores.md): fixed online store provider keys for `stores` fields and `preferred_store`; Huma `GET /api/v1/store-providers`
- [storage](modules/storage.md): local file store under DATA_DIR (`os.Root`, generated keys)
- [trash](modules/trash.md): folder, list and item deletes into trash batches (Huma), trash list, restore, purge, empty trash, retention auto-purge
- [tree](modules/tree.md): `GET /api/v1/tree`, the flat read of active folders and lists for the sidebar (handler only)
- [tmdb](modules/tmdb.md): bounded server-side TMDB client for movie and TV suggestions/details (`internal/integrations`)
- [workspace](modules/workspace.md): single-workspace bootstrap and settings service (Huma `GET`/`PATCH /api/v1/settings`)

Design docs (canonical): `/Users/federico/Developer/collections-app/design-docs/`

TMDB setup: get an API key through [TMDB account API settings](https://www.themoviedb.org/settings/api) and set `TMDB_API_KEY` in the server's `deploy/server/.env`; never place the key in browser configuration. The product must display an [approved TMDB logo and attribution](https://www.themoviedb.org/about/logos-attribution) in About. [TMDB watch-provider data](https://developer.themoviedb.org/reference/tv-series-watch-providers) requires JustWatch attribution. Movie and TV metadata imported from TMDB is refreshed weekly and never expires; the refresh overwrites user edits to those fields and keeps user images (D-123). Backups containing TMDB data must also be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
