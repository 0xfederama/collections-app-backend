-- F013-B4/B5 (API 0.9.0): localized TMDB metadata and movie director sorting.
ALTER TABLE workspace_settings ADD COLUMN metadata_language text NOT NULL DEFAULT 'en-US';
ALTER TABLE workspace_settings ADD CONSTRAINT workspace_settings_metadata_language_check CHECK (
  metadata_language ~ '^[a-z]{2}(-[A-Z]{2})?$'
);

ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (
  sort_mode IN ('manual', 'title', 'updated', 'kind_key', 'price', 'director')
);
