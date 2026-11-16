-- F-028 synopsis, TMDB score and genres (structure only, D-162, D-164). Expand-only: the previous
-- release never writes these columns, so its inserts and upserts keep working, and the new
-- columns default to NULL or 0.

-- The content version the lookup wrote for the item's description, and its SHA-256. The text is
-- still imported while the item's current version and hash both match (D-162 decision 3).
ALTER TABLE lookup_imports ADD COLUMN description_version integer, ADD COLUMN description_sha256 bytea;

-- The lookup import generation the row was read with (D-164). Rows below the current code
-- generation are re-read by the backfill; a new row gets the current one on every successful apply.
ALTER TABLE lookup_imports ADD COLUMN import_generation integer NOT NULL DEFAULT 0;

-- Who wrote a content version: NULL is a user save (and every row written before this migration),
-- 'lookup' is an import of a provider's description.
ALTER TABLE item_content_versions ADD COLUMN origin text;
ALTER TABLE item_content_versions ADD CONSTRAINT item_content_versions_origin_check CHECK (origin IN ('lookup'));
