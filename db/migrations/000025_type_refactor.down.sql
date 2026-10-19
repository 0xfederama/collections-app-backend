-- LOSSY (F-018). Restores the six-type built-in set shape, not the data: lists typed with the built-in
-- `vehicle` or `wishlist` get type NULL (their items keep metadata and price; the converted motorcycle,
-- car and camera lists are NOT recovered, and their dropped fields and prices are gone), the vehicle and
-- wishlist definitions are deleted, names go back to the singular seeded ones, book allows a price again,
-- and the seed functions return to the 000024 / 000021 versions, which re-create motorcycle, car and
-- camera (empty). A custom type renamed `<key>_custom` by the up migration is NOT renamed back.
UPDATE lists SET type = NULL, version = version + 1, updated_at = now()
WHERE type IN ('vehicle', 'wishlist') AND EXISTS (
  SELECT 1 FROM type_definitions d WHERE d.workspace_id = lists.workspace_id AND d.key = lists.type AND d.is_locked
);
DELETE FROM type_definitions WHERE key IN ('vehicle', 'wishlist') AND is_locked;

UPDATE type_definitions SET name = 'Movie', version = version + 1, updated_at = now()
WHERE key = 'movie' AND is_locked AND name = 'Movies';
UPDATE type_definitions SET name = 'TV show', version = version + 1, updated_at = now()
WHERE key = 'tv_show' AND is_locked AND name = 'TV shows';
UPDATE type_definitions SET name = 'Book', allows_price = true, version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND name = 'Books';
UPDATE type_definitions SET allows_price = true, version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND NOT allows_price;

CREATE OR REPLACE FUNCTION apply_builtin_type_default_pins(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE type_definitions k
  SET default_pinned_fields = s.pins::jsonb
  FROM (
  VALUES
    ('movie', '["runtime_minutes","streaming"]'),
    ('tv_show', '["seasons","streaming"]'),
    ('motorcycle', '["displacement_cc","price"]'),
    ('car', '["model_year","price"]'),
    ('camera', '["camera_type","price"]'),
    ('book', '["author","pages","stores"]')
  ) AS s(key, pins)
  WHERE k.workspace_id = target_workspace_id
    AND k.key = s.key
    AND k.is_locked
    AND k.default_pinned_fields IS NULL;
END;
$$;

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
    ('movie', 1, 'runtime_minutes', 'Runtime', 'number'),
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
