-- F013-B3: provenance and bounded retention for values imported from TMDB.
CREATE TABLE tmdb_imports (
  workspace_id      UUID        NOT NULL,
  item_id           UUID        NOT NULL,
  tmdb_id           INTEGER     NOT NULL,
  kind              TEXT        NOT NULL,
  imported_fields   JSONB       NOT NULL DEFAULT '{}',
  imported_title    TEXT        NULL,
  original_title    TEXT        NOT NULL,
  attachment_id     UUID        NULL,
  poster_path       TEXT        NULL,
  poster_fetched_at TIMESTAMPTZ NULL,
  poster_refresh_after TIMESTAMPTZ NULL,
  poster_expires_at TIMESTAMPTZ NULL,
  fetched_at        TIMESTAMPTZ NOT NULL,
  refresh_after     TIMESTAMPTZ NOT NULL,
  expires_at        TIMESTAMPTZ NOT NULL,
  CONSTRAINT tmdb_imports_pkey PRIMARY KEY (item_id),
  CONSTRAINT tmdb_imports_item_workspace_fkey FOREIGN KEY (item_id, workspace_id)
    REFERENCES items (id, workspace_id) ON DELETE CASCADE,
  CONSTRAINT tmdb_imports_tmdb_id_check CHECK (tmdb_id > 0),
  CONSTRAINT tmdb_imports_kind_check CHECK (kind IN ('movie', 'tv_show')),
  CONSTRAINT tmdb_imports_imported_fields_check CHECK (jsonb_typeof(imported_fields) = 'object'),
  CONSTRAINT tmdb_imports_imported_title_check CHECK (imported_title IS NULL OR char_length(imported_title) BETWEEN 1 AND 300),
  CONSTRAINT tmdb_imports_original_title_check CHECK (char_length(original_title) BETWEEN 1 AND 300),
  CONSTRAINT tmdb_imports_poster_check CHECK (
    (attachment_id IS NULL AND poster_path IS NULL AND poster_fetched_at IS NULL AND poster_refresh_after IS NULL AND poster_expires_at IS NULL)
    OR (attachment_id IS NOT NULL AND poster_path IS NOT NULL AND poster_fetched_at IS NOT NULL AND poster_refresh_after IS NOT NULL AND poster_expires_at IS NOT NULL)
  ),
  CONSTRAINT tmdb_imports_retention_check CHECK (fetched_at < refresh_after AND refresh_after < expires_at),
  CONSTRAINT tmdb_imports_poster_retention_check CHECK (
    poster_fetched_at IS NULL OR (poster_fetched_at < poster_refresh_after AND poster_refresh_after < poster_expires_at)
  )
);

CREATE INDEX tmdb_imports_due_idx ON tmdb_imports (workspace_id, refresh_after, item_id);
CREATE INDEX tmdb_imports_expiry_idx ON tmdb_imports (workspace_id, expires_at, item_id);
CREATE INDEX tmdb_imports_poster_expiry_idx ON tmdb_imports (workspace_id, poster_expires_at, item_id)
WHERE poster_expires_at IS NOT NULL;
CREATE INDEX tmdb_imports_poster_due_idx ON tmdb_imports (workspace_id, poster_refresh_after, item_id)
WHERE poster_refresh_after IS NOT NULL;
