-- F-016 cleanup (API 0.13.0, D-098 to D-100): three contract changes in one migration.
-- 1. The built-in `book` type loses `first_publish_year` (field row, item metadata, lookup
--    snapshots, list sort/pins, type default sort). Custom types (incl. a renamed
--    `book_custom`) are untouched: only locked `book` types and lists typed `book` are read.
-- 2. `openlibrary` stops being a store: stripped from every stores field of every item.
-- 3. `preferred_stores` (array) becomes `preferred_store` (one key, NOT NULL, default 'amazon').
-- Every step is idempotent: re-running changes nothing.

-- 2. Open Library is no longer a store. A stores field is any type_fields row of field_type
-- 'stores' (locked fields only, so the built-in book `stores` today; the loop does not assume it).
DO $$
DECLARE
  f record;
BEGIN
  FOR f IN
    SELECT DISTINCT d.workspace_id, d.key AS type_key, tf.key AS field_key
    FROM type_fields tf
    JOIN type_definitions d ON d.id = tf.type_id AND d.workspace_id = tf.workspace_id
    WHERE tf.field_type = 'stores'
  LOOP
    UPDATE items i
    SET metadata = jsonb_set(
          i.metadata, ARRAY[f.field_key],
          COALESCE((SELECT jsonb_agg(e ORDER BY o) FROM jsonb_array_elements(i.metadata -> f.field_key) WITH ORDINALITY AS t(e, o)
                    WHERE e <> '"openlibrary"'::jsonb), '[]'::jsonb)),
        version = i.version + 1,
        updated_at = now()
    FROM lists l
    WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id
      AND l.workspace_id = f.workspace_id AND l.type = f.type_key
      AND jsonb_typeof(i.metadata -> f.field_key) = 'array'
      AND i.metadata -> f.field_key @> '["openlibrary"]'::jsonb;
  END LOOP;
END;
$$;

-- 3. One default store. Backfill from the first remaining preferred store, else 'amazon'.
ALTER TABLE workspace_settings
  ADD COLUMN preferred_store TEXT NOT NULL DEFAULT 'amazon',
  ADD CONSTRAINT workspace_settings_preferred_store_check CHECK (
    preferred_store IN ('amazon', 'bookshop', 'apple_books', 'google_play_books', 'kobo')
  );
UPDATE workspace_settings
SET preferred_store = (array_remove(preferred_stores, 'openlibrary'))[1]
WHERE cardinality(array_remove(preferred_stores, 'openlibrary')) > 0;
ALTER TABLE workspace_settings
  DROP CONSTRAINT workspace_settings_preferred_stores_check,
  DROP COLUMN preferred_stores;

-- 1. Book: first publication year removed.
-- Lists sorted by it fall back to manual (the type default is now manual too); pins drop the key
-- and an emptied pin list becomes NULL (the type default).
UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = now()
WHERE type = 'book' AND sort_mode = 'first_publish_year';

UPDATE lists
SET pinned_fields = NULLIF(
      COALESCE((SELECT jsonb_agg(e ORDER BY o) FROM jsonb_array_elements(pinned_fields) WITH ORDINALITY AS t(e, o)
                WHERE e <> '"first_publish_year"'::jsonb), '[]'::jsonb), '[]'::jsonb),
    version = version + 1, updated_at = now()
WHERE type = 'book' AND jsonb_typeof(pinned_fields) = 'array' AND pinned_fields @> '["first_publish_year"]'::jsonb;

UPDATE items i
SET metadata = i.metadata - 'first_publish_year', version = i.version + 1, updated_at = now()
FROM lists l
WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id AND l.type = 'book'
  AND i.metadata ? 'first_publish_year';

UPDATE lookup_imports
SET imported_fields = imported_fields - 'first_publish_year'
WHERE type = 'book' AND imported_fields ? 'first_publish_year';

DELETE FROM type_fields tf
USING type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id
  AND d.key = 'book' AND d.is_locked AND tf.key = 'first_publish_year';

-- Close the position gap (positions are unique per type and the update is not row-ordered,
-- hence two steps through a high range).
UPDATE type_fields tf SET position = tf.position + 1000
FROM type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id AND d.key = 'book' AND d.is_locked
  AND tf.position > 2 AND NOT EXISTS (
    SELECT 1 FROM type_fields x WHERE x.type_id = tf.type_id AND x.position = 2);
UPDATE type_fields tf SET position = tf.position - 1001
FROM type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id AND d.key = 'book' AND d.is_locked
  AND tf.position >= 1000;

UPDATE type_definitions
SET default_sort_field = NULL,
    default_pinned_fields = CASE
      WHEN jsonb_typeof(default_pinned_fields) = 'array' AND default_pinned_fields @> '["first_publish_year"]'::jsonb
      THEN (SELECT jsonb_agg(e ORDER BY o) FROM jsonb_array_elements(default_pinned_fields) WITH ORDINALITY AS t(e, o)
            WHERE e <> '"first_publish_year"'::jsonb)
      ELSE default_pinned_fields END,
    version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND default_sort_field = 'first_publish_year';

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
    ('book', 'Book', true, NULL)
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
    ('book', 2, 'isbn', 'ISBN', 'text'),
    ('book', 3, 'stores', 'Where to get it', 'stores'),
    ('book', 4, 'openlibrary_id', 'Open Library ID', 'text'),
    ('book', 5, 'lookup_disabled', 'Disable lookup', 'boolean')
) AS s(type_key, position, key, label, field_type)
    ON k.key = s.type_key
  WHERE k.workspace_id = target_workspace_id
  ON CONFLICT (type_id, key) DO NOTHING;
END;
$$;

SELECT seed_builtin_type_definitions(id) FROM workspaces;
