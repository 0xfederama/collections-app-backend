# Docs index

- [api-contract](modules/api-contract.md): OpenAPI contract and generated Go server stubs
- [apperr](modules/apperr.md): typed domain errors, no HTTP imports
- [backup](modules/backup.md): Postgres plus images backup, restore and verify scripts (`scripts/`)
- [config](modules/config.md): env var parsing and startup validation
- [content](modules/content.md): item Markdown content, versions, restore and retention
- [database](modules/database.md): pgx pool, schema migrations, sqlc codegen
- [httpapi](modules/httpapi.md): HTTP router, probes, settings, tree/folder/list/item/content handlers
- [images](modules/images.md): item image upload/URL/delete, metadata stripping (`strip-images` backfill), validation, thumbnails, serving, orphan sweeper
- [items](modules/items.md): items CRUD, move and reorder, per-kind metadata validation (`items/kinds`)
- [search](modules/search.md): item title search with folder and list paths (`GET /api/v1/search`) and recent items (`GET /api/v1/recent-items`)
- [smoke](modules/smoke.md): HTTP smoke test over a running stack (`scripts/smoke.sh`, `make smoke`)
- [storage](modules/storage.md): local file store under DATA_DIR (`os.Root`, generated keys)
- [trash](modules/trash.md): delete into trash batches, trash list, restore, purge
- [tree](modules/tree.md): folders and lists CRUD, move with cycle check, reorder, sidebar tree
- [workspace](modules/workspace.md): single-workspace bootstrap and settings service

Design docs (canonical): `/Users/federico/Developer/collections-app/design-docs/`
