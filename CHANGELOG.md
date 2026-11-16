# Changelog

Backend releases (`vX.Y.Z` tags, D-043). Contract milestones are `api-vX.Y.Z` tags; see [api-contract](docs/modules/api-contract.md).

## 3.2.0 (2026-11-16)

Contract: `api-v4.3.0` (additive). Migration 000003 (expand-only, D-118).

### Added
- Movies and TV: the TMDB score (`tmdb_rating`, from ten votes, one decimal), the vote count (`tmdb_vote_count`) and the genres (`genres`, a new `text_list` field type). Imported only, never client-written (F-028 B1, D-162).
- Descriptions: the TMDB overview (English when the metadata language has none), the Open Library work description and the Hardcover description on creates are written into an empty description as a content version with `origin: lookup`. A later re-match or refresh replaces that text until the user edits it (F-028 B2, D-162).
- Content: read-only `origin` (`user` or `lookup`) on the current content and on each version.
- Lists: `GET /api/v1/lists/{id}/items?sort=rating` (movie and TV: highest TMDB score first, unrated last), the repeatable `genre` filter (any of, case-insensitive), `rating` as a list `sort_mode`, and `GET /api/v1/lists/{id}/genres` with the genre counts (F-028 B3, D-163).
- A generic backfill re-reads every provider-linked item once: ten-minute passes of 25 items, resumable, throttled, stopped by a rate limit (F-028 B4, D-164).

### Changed
- Writing `tmdb_rating`, `tmdb_vote_count` or `genres` with a different value is 422 `validation_failed`. An omitted key keeps the stored value.
- `invalid_sort` and `invalid_filter` (422) join the problem codes.

### Migration
- `000003_synopsis_rating_genres`: `lookup_imports` gains `description_version`, `description_sha256` and `import_generation`; `item_content_versions` gains `origin` with a check for `'lookup'`. Nothing is backfilled in SQL: the generic backfill re-reads the items.

### Fixed
- `PATCH /items/{id}`: a metadata object without `tmdb_id`, `openlibrary_id` or `lookup_disabled` keeps the stored value instead of dropping it (an edit used to unlink the item from its provider). An explicit value, `null` included, is still written.
