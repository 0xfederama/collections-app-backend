-- F-016 (D-096): lookup providers are generalised. `tmdb_imports` becomes `lookup_imports`
-- with a `provider` and a text `source_id` (the TMDB id for tmdb rows, a work id such as
-- OL45804W for openlibrary rows). Existing rows are kept as provider 'tmdb'. `tmdb_id` is
-- dropped after the backfill (its value lives on as source_id).
-- Open Library imports carry no retention duty (D-095): their refresh_after, expires_at and
-- poster_refresh_after / poster_expires_at are NULL, and the retention maintenance (which
-- only looks at non-NULL deadlines of provider 'tmdb') never touches them. The CHECKs below
-- make both shapes the only valid ones.
-- Constraint and index names follow <table>_<cols>_{pkey,key,fkey,check,idx}.
ALTER TABLE tmdb_imports RENAME TO lookup_imports;

ALTER TABLE lookup_imports ADD COLUMN provider  TEXT NOT NULL DEFAULT 'tmdb';
ALTER TABLE lookup_imports ADD COLUMN source_id TEXT NULL;
UPDATE lookup_imports SET source_id = tmdb_id::text;
ALTER TABLE lookup_imports ALTER COLUMN source_id SET NOT NULL;
ALTER TABLE lookup_imports DROP COLUMN tmdb_id; -- also drops tmdb_imports_tmdb_id_check

ALTER TABLE lookup_imports ALTER COLUMN refresh_after DROP NOT NULL;
ALTER TABLE lookup_imports ALTER COLUMN expires_at DROP NOT NULL;

ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_pkey TO lookup_imports_pkey;
ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_item_workspace_fkey TO lookup_imports_item_workspace_fkey;
ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_imported_fields_check TO lookup_imports_imported_fields_check;
ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_imported_title_check TO lookup_imports_imported_title_check;
ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_original_title_check TO lookup_imports_original_title_check;
ALTER TABLE lookup_imports RENAME CONSTRAINT tmdb_imports_poster_retention_check TO lookup_imports_poster_retention_check;

ALTER TABLE lookup_imports DROP CONSTRAINT tmdb_imports_type_check;
ALTER TABLE lookup_imports DROP CONSTRAINT tmdb_imports_poster_check;
ALTER TABLE lookup_imports DROP CONSTRAINT tmdb_imports_retention_check;

ALTER TABLE lookup_imports
  ADD CONSTRAINT lookup_imports_provider_check CHECK (provider IN ('tmdb', 'openlibrary')),
  ADD CONSTRAINT lookup_imports_type_check CHECK (type IN ('movie', 'tv_show', 'book')),
  ADD CONSTRAINT lookup_imports_provider_type_check CHECK (
    (provider = 'tmdb' AND type IN ('movie', 'tv_show')) OR (provider = 'openlibrary' AND type = 'book')
  ),
  ADD CONSTRAINT lookup_imports_source_id_check CHECK (
    char_length(source_id) BETWEEN 1 AND 64
    AND (provider <> 'tmdb' OR source_id ~ '^[1-9][0-9]{0,9}$')
    AND (provider <> 'openlibrary' OR source_id ~ '^OL[0-9]+W$')
  ),
  ADD CONSTRAINT lookup_imports_retention_check CHECK (
    (provider = 'tmdb' AND refresh_after IS NOT NULL AND expires_at IS NOT NULL
       AND fetched_at < refresh_after AND refresh_after < expires_at)
    OR (provider <> 'tmdb' AND refresh_after IS NULL AND expires_at IS NULL)
  ),
  ADD CONSTRAINT lookup_imports_poster_check CHECK (
    (attachment_id IS NULL AND poster_path IS NULL AND poster_fetched_at IS NULL
       AND poster_refresh_after IS NULL AND poster_expires_at IS NULL)
    OR (attachment_id IS NOT NULL AND poster_path IS NOT NULL AND poster_fetched_at IS NOT NULL AND (
         (provider = 'tmdb' AND poster_refresh_after IS NOT NULL AND poster_expires_at IS NOT NULL)
         OR (provider <> 'tmdb' AND poster_refresh_after IS NULL AND poster_expires_at IS NULL)))
  );

ALTER INDEX tmdb_imports_due_idx RENAME TO lookup_imports_due_idx;
ALTER INDEX tmdb_imports_expiry_idx RENAME TO lookup_imports_expiry_idx;
ALTER INDEX tmdb_imports_poster_expiry_idx RENAME TO lookup_imports_poster_expiry_idx;
ALTER INDEX tmdb_imports_poster_due_idx RENAME TO lookup_imports_poster_due_idx;
