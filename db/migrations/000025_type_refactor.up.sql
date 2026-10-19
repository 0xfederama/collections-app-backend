-- F-018 (API 0.16.0): the built-in type set is exactly five, named in the plural:
-- book 'Books', vehicle 'Vehicles', movie 'Movies', tv_show 'TV shows', wishlist 'Wishlist'.
-- * motorcycle and car lists become `vehicle`; camera is removed (its lists become no-type).
-- * price is allowed on `wishlist` only among the built-ins (book loses it).
-- * a custom type keyed `vehicle` or `wishlist` is renamed `<key>_custom` first (D-097 style).
-- Fields Vehicles lack (license_class, fuel_type, camera fields) and every price on a vehicle,
-- camera or book item are discarded (production unused; no description block). Only lists typed
-- with the old keys are converted, so re-running never touches data a user entered on Vehicles
-- (including their own custom fields). Every step is idempotent.

-- Collision: only non-locked types can collide (the old locked keys are not vehicle/wishlist).
-- lists.type is the only column holding a type key besides the definition (lookup_imports.type
-- is movie/tv_show/book only), so the lists follow. Fields, items and metadata are untouched.
DO $$
DECLARE
  r record;
  new_key text;
  n integer;
BEGIN
  FOR r IN SELECT id, workspace_id, key FROM type_definitions WHERE key IN ('vehicle', 'wishlist') AND NOT is_locked LOOP
    new_key := r.key || '_custom';
    n := 1;
    WHILE EXISTS (SELECT 1 FROM type_definitions WHERE workspace_id = r.workspace_id AND key = new_key) LOOP
      n := n + 1;
      new_key := r.key || '_custom_' || n;
    END LOOP;
    UPDATE type_definitions SET key = new_key, version = version + 1, updated_at = now() WHERE id = r.id;
    UPDATE lists SET type = new_key, version = version + 1, updated_at = now()
    WHERE workspace_id = r.workspace_id AND type = r.key;
  END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION apply_builtin_type_default_pins(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE type_definitions k
  SET default_pinned_fields = s.pins::jsonb
  FROM (
  VALUES
    ('movie', '["runtime_minutes","streaming"]'),
    ('tv_show', '["seasons","streaming"]'),
    ('book', '["author","pages","stores"]'),
    ('vehicle', '["model_year","power_hp"]'),
    ('wishlist', '["price"]')
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
  INSERT INTO type_definitions (workspace_id, key, name, default_icon, allows_price, default_sort_field, is_locked)
  SELECT target_workspace_id, s.key, s.name, s.default_icon, s.allows_price, s.default_sort_field, true
  FROM (
  VALUES
    ('movie', 'Movies', NULL::text, false, 'release_year'),
    ('tv_show', 'TV shows', NULL, false, 'first_air_year'),
    ('book', 'Books', NULL, false, NULL),
    ('vehicle', 'Vehicles', 'i:car', false, NULL),
    ('wishlist', 'Wishlist', 'i:tag', true, NULL)
) AS s(key, name, default_icon, allows_price, default_sort_field)
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
    ('book', 0, 'author', 'Writer', 'text'),
    ('book', 1, 'pages', 'Pages', 'number'),
    ('book', 2, 'isbn', 'ISBN', 'text'),
    ('book', 3, 'stores', 'Where to get it', 'stores'),
    ('book', 4, 'openlibrary_id', 'Open Library ID', 'text'),
    ('book', 5, 'lookup_disabled', 'Disable lookup', 'boolean'),
    ('vehicle', 0, 'make', 'Make', 'text'),
    ('vehicle', 1, 'model', 'Model', 'text'),
    ('vehicle', 2, 'model_year', 'Year', 'number'),
    ('vehicle', 3, 'displacement_cc', 'Engine size (cc)', 'number'),
    ('vehicle', 4, 'power_hp', 'Power (hp)', 'number')
) AS s(type_key, position, key, label, field_type)
    ON k.key = s.type_key
  WHERE k.workspace_id = target_workspace_id
  ON CONFLICT (type_id, key) DO NOTHING;
END;
$$;

-- The new built-ins (vehicle, wishlist) for existing workspaces, before lists are converted.
SELECT seed_builtin_type_definitions(id) FROM workspaces;

-- Motorcycle and car items: keep only the keys Vehicles has, drop every price. Done before the
-- lists change type (the join reads the old type).
UPDATE items i
SET metadata = COALESCE((SELECT jsonb_object_agg(e.key, e.value) FROM jsonb_each(i.metadata) AS e
                         WHERE e.key IN ('make', 'model', 'model_year', 'displacement_cc', 'power_hp')), '{}'::jsonb),
    price_min = NULL, price_max = NULL, currency = NULL,
    version = i.version + 1, updated_at = now()
FROM lists l
WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id AND l.type IN ('motorcycle', 'car');

-- Camera items: no type means empty metadata and no price.
UPDATE items i
SET metadata = '{}'::jsonb, price_min = NULL, price_max = NULL, currency = NULL,
    version = i.version + 1, updated_at = now()
FROM lists l
WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id AND l.type = 'camera';

-- Book items lose their price (price is allowed on wishlist only among the built-ins).
UPDATE items i
SET price_min = NULL, price_max = NULL, currency = NULL, version = i.version + 1, updated_at = now()
FROM lists l
WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id AND l.type = 'book'
  AND (i.price_min IS NOT NULL OR i.price_max IS NOT NULL OR i.currency IS NOT NULL);

-- Motorcycle and car lists become vehicle lists. A sort on a dropped field (or price) falls back
-- to manual; pins keep only keys Vehicles has (an emptied list becomes NULL, the type default).
UPDATE lists
SET type = 'vehicle',
    sort_mode = CASE WHEN sort_mode IN ('manual', 'title', 'updated', 'make', 'model', 'model_year', 'displacement_cc', 'power_hp')
                     THEN sort_mode ELSE 'manual' END,
    pinned_fields = CASE WHEN jsonb_typeof(pinned_fields) = 'array' THEN NULLIF(
        COALESCE((SELECT jsonb_agg(e ORDER BY o) FROM jsonb_array_elements(pinned_fields) WITH ORDINALITY AS t(e, o)
                  WHERE (e #>> '{}') IN ('make', 'model', 'model_year', 'displacement_cc', 'power_hp')), '[]'::jsonb),
        '[]'::jsonb) ELSE NULL END,
    version = version + 1, updated_at = now()
WHERE type IN ('motorcycle', 'car');

-- Camera lists become no-type lists: built-in sort modes only, no pins. Icon etc. untouched.
UPDATE lists
SET type = NULL,
    sort_mode = CASE WHEN sort_mode IN ('manual', 'title', 'updated') THEN sort_mode ELSE 'manual' END,
    pinned_fields = NULL,
    version = version + 1, updated_at = now()
WHERE type = 'camera';

-- Book lists: no price sort, no price pin (as 000023 did for first_publish_year).
UPDATE lists SET sort_mode = 'manual', version = version + 1, updated_at = now()
WHERE type = 'book' AND sort_mode = 'price';

UPDATE lists
SET pinned_fields = NULLIF(
      COALESCE((SELECT jsonb_agg(e ORDER BY o) FROM jsonb_array_elements(pinned_fields) WITH ORDINALITY AS t(e, o)
                WHERE e <> '"price"'::jsonb), '[]'::jsonb), '[]'::jsonb),
    version = version + 1, updated_at = now()
WHERE type = 'book' AND jsonb_typeof(pinned_fields) = 'array' AND pinned_fields @> '["price"]'::jsonb;

-- The old definitions go (type_fields follow through ON DELETE CASCADE).
DELETE FROM type_definitions WHERE key IN ('motorcycle', 'car', 'camera') AND is_locked;

-- Plural names: only rows still carrying the seeded singular name, so a customised name is kept.
UPDATE type_definitions SET name = 'Movies', version = version + 1, updated_at = now()
WHERE key = 'movie' AND is_locked AND name = 'Movie';
UPDATE type_definitions SET name = 'TV shows', version = version + 1, updated_at = now()
WHERE key = 'tv_show' AND is_locked AND name = 'TV show';
UPDATE type_definitions SET name = 'Books', version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND name = 'Book';

-- Price is allowed on wishlist only among the built-ins.
UPDATE type_definitions SET allows_price = false, version = version + 1, updated_at = now()
WHERE key = 'book' AND is_locked AND allows_price;
