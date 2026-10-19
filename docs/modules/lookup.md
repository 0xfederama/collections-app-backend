# lookup

## Purpose

Defines the merge and provenance boundary for importing TMDB values into movie and TV items. It records imported snapshots and expiry in PostgreSQL; design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/lookup/types.go`: lookup modes, provenance `Record`, `ApplyParams`, and `Store` contract
- `db/queries/tmdb_imports.sql`: workspace-scoped locking, provenance, due and expiry queries
- `db/migrations/000012_tmdb_imports.up.sql`: imported field and poster provenance table
- `internal/httpapi/items.go`: item create and lookup handler integration
- `cmd/api/main.go`: lookup service and maintenance wiring

## Interfaces

- `Mode`: `create`, `fill`, `rematch`, `refresh`, and automatic `owned` refresh
- `Store.ListType`, `Store.Read`, `Store.Apply`: list validation, provenance read, atomic item/image/provenance apply
- `GET /api/v1/tmdb/suggestions`: search by `movie|tv_show` and query
- `POST /api/v1/items/{id}/lookup`: explicit fill/rematch/refresh with item `If-Match`
- Movie/TV item creation may request lookup; a missing server key defaults to creating the typed original item

## Gotchas

- Refresh before six-month expiry (scheduled at five months); field snapshots allow replacing only values still equal to their imported values.
- User edits survive refresh and expiry, including on trashed items. If an unchanged imported title expires, restore the original typed title.
- Poster expiry is tracked independently from metadata refresh; manual lookup modes preserve the existing image.
- Backups containing TMDB data must be retired within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use).
