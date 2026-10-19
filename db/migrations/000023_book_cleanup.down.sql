-- LOSSY: `first_publish_year` values stripped from items and lookup snapshots are not restored,
-- nor are `openlibrary` store selections or the list sorts/pins reset by the up migration.
-- Restored: the book field row (position 2, locked), the old seed, the book default sort, and
-- `preferred_stores` (the single store becomes a one-element list).
ALTER TABLE workspace_settings
  ADD COLUMN preferred_stores TEXT[] NOT NULL DEFAULT '{}',
  ADD CONSTRAINT workspace_settings_preferred_stores_check CHECK (
    preferred_stores <@ ARRAY['openlibrary', 'bookshop', 'amazon', 'apple_books', 'google_play_books', 'kobo']::text[]
  );
UPDATE workspace_settings SET preferred_stores = ARRAY[preferred_store];
ALTER TABLE workspace_settings
  DROP CONSTRAINT workspace_settings_preferred_store_check,
  DROP COLUMN preferred_store;

UPDATE type_fields tf SET position = tf.position + 1000
FROM type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id AND d.key = 'book' AND d.is_locked
  AND tf.position >= 2 AND NOT EXISTS (
    SELECT 1 FROM type_fields x WHERE x.type_id = tf.type_id AND x.key = 'first_publish_year');
UPDATE type_fields tf SET position = tf.position - 999
FROM type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id AND d.key = 'book' AND d.is_locked
  AND tf.position >= 1000;
INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position, is_locked)
SELECT d.workspace_id, d.id, 'first_publish_year', 'First publication year', 'number', 2, true
FROM type_definitions d
WHERE d.key = 'book' AND d.is_locked
ON CONFLICT (type_id, key) DO NOTHING;

UPDATE type_definitions
SET default_sort_field = 'first_publish_year', version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND default_sort_field IS NULL;

CREATE OR REPLACE FUNCTION seed_builtin_type_definitions(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO type_definitions (workspace_id, key, name, allows_price, default_sort_field, is_locked)
  SELECT target_workspace_id, s.key, s.name, s.allows_price, s.default_sort_field, true
  FROM (
  VALUES
    ('movie', 'Movie', false, 'release_year'),
    ('tv_show', 'TV show', false, 'first_air_year'),
    ('motorcycle', 'Motorcycle', true, 'displacement_cc'),
    ('car', 'Car', true, 'power_hp'),
    ('camera', 'Camera', true, 'megapixels'),
    ('book', 'Book', true, 'first_publish_year')
) AS s(key, name, allows_price, default_sort_field)
  ON CONFLICT (workspace_id, key) DO NOTHING;

  PERFORM apply_builtin_type_default_pins(target_workspace_id);

  INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position, is_locked)
  SELECT k.workspace_id, k.id, s.key, s.label, s.field_type, s.position, true
  FROM type_definitions k
  JOIN (
  VALUES
    ('movie', 0, 'release_year', 'Release year', 'number'),
    ('movie', 1, 'runtime_minutes', 'Runtime (minutes)', 'number'),
    ('movie', 2, 'director', 'Director', 'text'),
    ('movie', 3, 'imdb_url', 'IMDb URL', 'link'),
    ('movie', 4, 'streaming', 'Streaming', 'streaming'),
    ('movie', 5, 'tmdb_id', 'TMDB ID', 'number'),
    ('movie', 6, 'lookup_disabled', 'Disable lookup', 'boolean'),
    ('tv_show', 0, 'first_air_year', 'First air year', 'number'),
    ('tv_show', 1, 'end_year', 'End year', 'number'),
    ('tv_show', 2, 'seasons', 'Seasons', 'number'),
    ('tv_show', 3, 'episodes_per_season', 'Episodes per season', 'number'),
    ('tv_show', 4, 'episode_minutes', 'Episode minutes', 'number'),
    ('tv_show', 5, 'imdb_url', 'IMDb URL', 'link'),
    ('tv_show', 6, 'streaming', 'Streaming', 'streaming'),
    ('tv_show', 7, 'tmdb_id', 'TMDB ID', 'number'),
    ('tv_show', 8, 'lookup_disabled', 'Disable lookup', 'boolean'),
    ('motorcycle', 0, 'model_year', 'Model year', 'number'),
    ('motorcycle', 1, 'displacement_cc', 'Displacement (cc)', 'number'),
    ('motorcycle', 2, 'power_hp', 'Power (hp)', 'number'),
    ('motorcycle', 3, 'license_class', 'License class', 'text'),
    ('car', 0, 'make', 'Make', 'text'),
    ('car', 1, 'model', 'Model', 'text'),
    ('car', 2, 'model_year', 'Model year', 'number'),
    ('car', 3, 'fuel_type', 'Fuel type', 'text'),
    ('car', 4, 'power_hp', 'Power (hp)', 'number'),
    ('camera', 0, 'camera_type', 'Camera type', 'text'),
    ('camera', 1, 'release_year', 'Release year', 'number'),
    ('camera', 2, 'sensor_format', 'Sensor format', 'text'),
    ('camera', 3, 'megapixels', 'Megapixels', 'number'),
    ('book', 0, 'author', 'Writer', 'text'),
    ('book', 1, 'pages', 'Pages', 'number'),
    ('book', 2, 'first_publish_year', 'First publication year', 'number'),
    ('book', 3, 'isbn', 'ISBN', 'text'),
    ('book', 4, 'stores', 'Where to get it', 'stores'),
    ('book', 5, 'openlibrary_id', 'Open Library ID', 'text'),
    ('book', 6, 'lookup_disabled', 'Disable lookup', 'boolean')
) AS s(type_key, position, key, label, field_type)
    ON k.key = s.type_key
  WHERE k.workspace_id = target_workspace_id
  ON CONFLICT (type_id, key) DO NOTHING;
END;
$$;
