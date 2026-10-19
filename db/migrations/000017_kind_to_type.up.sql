-- Rename the concept "kind" to "type" (API 0.11.0). Data-preserving: tables,
-- columns, constraints, indexes, functions and the trigger are renamed; the
-- built-in keys (movie, tv_show, ...) and all stored data are unchanged. The
-- `type_fields.field_type` column (text/number/date/...) keeps its name.
-- Constraint names stay explicit (<table>_<cols>_{pkey,key,fkey,check,idx}).
DROP TRIGGER workspaces_seed_builtin_kind_definitions ON workspaces;
DROP FUNCTION seed_builtin_kind_definitions_on_workspace_insert();
DROP FUNCTION seed_builtin_kind_definitions(uuid);
DROP FUNCTION apply_builtin_kind_default_pins(uuid);

ALTER TABLE kind_definitions RENAME TO type_definitions;
ALTER TABLE kind_fields RENAME TO type_fields;
ALTER TABLE type_fields RENAME COLUMN kind_id TO type_id;
ALTER TABLE lists RENAME COLUMN kind TO type;
ALTER TABLE tmdb_imports RENAME COLUMN kind TO type;

ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_pkey TO type_definitions_pkey;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_id_workspace_id_key TO type_definitions_id_workspace_id_key;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_workspace_id_key_key TO type_definitions_workspace_id_key_key;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_workspace_id_fkey TO type_definitions_workspace_id_fkey;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_key_check TO type_definitions_key_check;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_name_check TO type_definitions_name_check;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_default_sort_field_check TO type_definitions_default_sort_field_check;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_default_pinned_fields_check TO type_definitions_default_pinned_fields_check;
ALTER TABLE type_definitions RENAME CONSTRAINT kind_definitions_version_check TO type_definitions_version_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_pkey TO type_fields_pkey;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_id_workspace_id_key TO type_fields_id_workspace_id_key;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_kind_id_key_key TO type_fields_type_id_key_key;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_kind_id_position_key TO type_fields_type_id_position_key;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_kind_workspace_fkey TO type_fields_type_workspace_fkey;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_key_check TO type_fields_key_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_label_check TO type_fields_label_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_field_type_check TO type_fields_field_type_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_custom_field_type_check TO type_fields_custom_field_type_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_position_check TO type_fields_position_check;
ALTER TABLE type_fields RENAME CONSTRAINT kind_fields_version_check TO type_fields_version_check;
ALTER TABLE lists RENAME CONSTRAINT lists_kind_check TO lists_type_check;
ALTER TABLE tmdb_imports RENAME CONSTRAINT tmdb_imports_kind_check TO tmdb_imports_type_check;
ALTER INDEX kind_definitions_workspace_idx RENAME TO type_definitions_workspace_idx;
ALTER INDEX kind_fields_workspace_kind_position_idx RENAME TO type_fields_workspace_type_position_idx;

CREATE FUNCTION apply_builtin_type_default_pins(target_workspace_id uuid) RETURNS void
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
    ('generic', '[]')
  ) AS s(key, pins)
  WHERE k.workspace_id = target_workspace_id
    AND k.key = s.key
    AND k.is_locked
    AND k.default_pinned_fields IS NULL;
END;
$$;

CREATE FUNCTION seed_builtin_type_definitions(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO type_definitions (workspace_id, key, name, allows_price, default_sort_field, is_locked)
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
    ('camera', 3, 'megapixels', 'Megapixels', 'number')
) AS s(type_key, position, key, label, field_type)
    ON k.key = s.type_key
  WHERE k.workspace_id = target_workspace_id
  ON CONFLICT (type_id, key) DO NOTHING;
END;
$$;

CREATE FUNCTION seed_builtin_type_definitions_on_workspace_insert() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM seed_builtin_type_definitions(NEW.id);
  RETURN NEW;
END;
$$;

CREATE TRIGGER workspaces_seed_builtin_type_definitions
AFTER INSERT ON workspaces
FOR EACH ROW EXECUTE FUNCTION seed_builtin_type_definitions_on_workspace_insert();
