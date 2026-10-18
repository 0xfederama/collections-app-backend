-- F014-B1: workspace-scoped kind definitions replace the fixed registry.
-- The six existing kinds and their metadata keys are immutable seed data. The
-- `streaming` type is reserved for the existing structured movie/TV value;
-- custom fields are limited to the five ordinary types by the service.
CREATE TABLE kind_definitions (
  id                    UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id          UUID        NOT NULL,
  key                   TEXT        NOT NULL,
  name                  TEXT        NOT NULL,
  default_icon          TEXT        NULL,
  allows_price          BOOLEAN     NOT NULL DEFAULT false,
  default_sort_field    TEXT        NULL,
  default_pinned_fields JSONB       NULL,
  is_locked             BOOLEAN     NOT NULL DEFAULT false,
  version               INTEGER     NOT NULL DEFAULT 1,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT kind_definitions_pkey PRIMARY KEY (id),
  CONSTRAINT kind_definitions_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT kind_definitions_workspace_id_key_key UNIQUE (workspace_id, key),
  CONSTRAINT kind_definitions_workspace_id_fkey FOREIGN KEY (workspace_id)
    REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT kind_definitions_key_check CHECK (key ~ '^[a-z][a-z0-9_]{0,31}$'),
  CONSTRAINT kind_definitions_name_check CHECK (char_length(name) BETWEEN 1 AND 100),
  CONSTRAINT kind_definitions_default_sort_field_check CHECK (
    default_sort_field IS NULL OR default_sort_field ~ '^[a-z][a-z0-9_]{0,31}$'
  ),
  CONSTRAINT kind_definitions_default_pinned_fields_check CHECK (
    default_pinned_fields IS NULL OR
    (jsonb_typeof(default_pinned_fields) = 'array' AND jsonb_array_length(default_pinned_fields) <= 2)
  ),
  CONSTRAINT kind_definitions_version_check CHECK (version >= 1)
);

CREATE TABLE kind_fields (
  id           UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id UUID        NOT NULL,
  kind_id      UUID        NOT NULL,
  key          TEXT        NOT NULL,
  label        TEXT        NOT NULL,
  field_type   TEXT        NOT NULL,
  position     INTEGER     NOT NULL,
  is_locked    BOOLEAN     NOT NULL DEFAULT false,
  version      INTEGER     NOT NULL DEFAULT 1,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT kind_fields_pkey PRIMARY KEY (id),
  CONSTRAINT kind_fields_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT kind_fields_kind_id_key_key UNIQUE (kind_id, key),
  CONSTRAINT kind_fields_kind_id_position_key UNIQUE (kind_id, position),
  CONSTRAINT kind_fields_kind_workspace_fkey FOREIGN KEY (kind_id, workspace_id)
    REFERENCES kind_definitions (id, workspace_id) ON DELETE CASCADE,
  CONSTRAINT kind_fields_key_check CHECK (key ~ '^[a-z][a-z0-9_]{0,31}$'),
  CONSTRAINT kind_fields_label_check CHECK (char_length(label) BETWEEN 1 AND 100),
  CONSTRAINT kind_fields_field_type_check CHECK (
    field_type IN ('text', 'number', 'date', 'boolean', 'link', 'streaming')
  ),
  CONSTRAINT kind_fields_custom_field_type_check CHECK (
    is_locked OR field_type IN ('text', 'number', 'date', 'boolean', 'link')
  ),
  CONSTRAINT kind_fields_position_check CHECK (position >= 0),
  CONSTRAINT kind_fields_version_check CHECK (version >= 1)
);

CREATE INDEX kind_definitions_workspace_idx ON kind_definitions (workspace_id, key);
CREATE INDEX kind_fields_workspace_kind_position_idx ON kind_fields (workspace_id, kind_id, position);

-- Called by workspace bootstrap as well as this migration, so a fresh
-- deployment receives the definitions after its workspace is created.
CREATE FUNCTION seed_builtin_kind_definitions(target_workspace_id uuid) RETURNS void
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

-- One set of immutable definitions per existing workspace. This does not
-- change item metadata or list keys.
SELECT seed_builtin_kind_definitions(id) FROM workspaces;
