UPDATE kind_definitions SET default_pinned_fields = NULL WHERE is_locked;

CREATE OR REPLACE FUNCTION seed_builtin_kind_definitions(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO kind_definitions (workspace_id, key, name, allows_price, default_sort_field, is_locked)
  SELECT target_workspace_id, s.key, s.name, s.allows_price, s.default_sort_field, true
  FROM (
  VALUES
    ('generic', 'Generic', false, NULL::text),
    ('movie', 'Movie', false, 'release_year'),
    ('tv_show', 'TV show', false, 'first_air_year'),
    ('motorcycle', 'Motorcycle', true, 'displacement_cc'),
    ('car', 'Car', true, 'power_hp'),
    ('camera', 'Camera', true, 'megapixels')
) AS s(key, name, allows_price, default_sort_field)
  ON CONFLICT (workspace_id, key) DO NOTHING;

  INSERT INTO kind_fields (workspace_id, kind_id, key, label, field_type, position, is_locked)
  SELECT k.workspace_id, k.id, s.key, s.label, s.field_type, s.position, true
  FROM kind_definitions k
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
    ('camera', 3, 'megapixels', 'Megapixels', 'number')
) AS s(kind_key, position, key, label, field_type)
    ON k.key = s.kind_key
  WHERE k.workspace_id = target_workspace_id
  ON CONFLICT (kind_id, key) DO NOTHING;
END;
$$;


DROP FUNCTION apply_builtin_kind_default_pins(uuid);
