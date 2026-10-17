-- `director` does not exist before F013. Reset affected lists before restoring the old CHECK.
UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = now()
WHERE sort_mode = 'director';

ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (
  sort_mode IN ('manual', 'title', 'updated', 'kind_key', 'price')
);

ALTER TABLE workspace_settings DROP CONSTRAINT workspace_settings_metadata_language_check;
ALTER TABLE workspace_settings DROP COLUMN metadata_language;
