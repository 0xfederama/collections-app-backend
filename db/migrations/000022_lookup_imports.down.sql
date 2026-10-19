-- LOSSY (F-016). Back to TMDB-only provenance: rows of any other provider (the Open Library
-- book imports) are DELETED, because `tmdb_imports` can only describe TMDB imports. The
-- items themselves are untouched (their metadata and cover stay; they simply lose
-- the record of what was imported, so a later lookup treats the values as the user's own).
-- tmdb rows are kept: source_id goes back to the integer tmdb_id.
DELETE FROM lookup_imports WHERE provider <> 'tmdb';

ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_provider_check;
ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_type_check;
ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_provider_type_check;
ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_source_id_check;
ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_retention_check;
ALTER TABLE lookup_imports DROP CONSTRAINT lookup_imports_poster_check;

ALTER TABLE lookup_imports ADD COLUMN tmdb_id INTEGER NULL;
UPDATE lookup_imports SET tmdb_id = source_id::integer;
ALTER TABLE lookup_imports ALTER COLUMN tmdb_id SET NOT NULL;
ALTER TABLE lookup_imports DROP COLUMN source_id;
ALTER TABLE lookup_imports DROP COLUMN provider;
ALTER TABLE lookup_imports ALTER COLUMN refresh_after SET NOT NULL;
ALTER TABLE lookup_imports ALTER COLUMN expires_at SET NOT NULL;

ALTER TABLE lookup_imports RENAME TO tmdb_imports;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_pkey TO tmdb_imports_pkey;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_item_workspace_fkey TO tmdb_imports_item_workspace_fkey;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_imported_fields_check TO tmdb_imports_imported_fields_check;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_imported_title_check TO tmdb_imports_imported_title_check;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_original_title_check TO tmdb_imports_original_title_check;
ALTER TABLE tmdb_imports RENAME CONSTRAINT lookup_imports_poster_retention_check TO tmdb_imports_poster_retention_check;

ALTER TABLE tmdb_imports
  ADD CONSTRAINT tmdb_imports_tmdb_id_check CHECK (tmdb_id > 0),
  ADD CONSTRAINT tmdb_imports_type_check CHECK (type IN ('movie', 'tv_show')),
  ADD CONSTRAINT tmdb_imports_retention_check CHECK (fetched_at < refresh_after AND refresh_after < expires_at),
  ADD CONSTRAINT tmdb_imports_poster_check CHECK (
    (attachment_id IS NULL AND poster_path IS NULL AND poster_fetched_at IS NULL AND poster_refresh_after IS NULL AND poster_expires_at IS NULL)
    OR (attachment_id IS NOT NULL AND poster_path IS NOT NULL AND poster_fetched_at IS NOT NULL AND poster_refresh_after IS NOT NULL AND poster_expires_at IS NOT NULL)
  );

ALTER INDEX lookup_imports_due_idx RENAME TO tmdb_imports_due_idx;
ALTER INDEX lookup_imports_expiry_idx RENAME TO tmdb_imports_expiry_idx;
ALTER INDEX lookup_imports_poster_expiry_idx RENAME TO tmdb_imports_poster_expiry_idx;
ALTER INDEX lookup_imports_poster_due_idx RENAME TO tmdb_imports_poster_due_idx;
