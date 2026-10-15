-- F-007 restructure (API 0.3.0): one kind per list, trimmed item fields, Markdown list description.
-- Data is moved in plain SQL before the columns are dropped: nothing is lost (see the steps below).

-- Helpers, dropped at the end of this file.
CREATE FUNCTION f007_kind_label(kind text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE kind
    WHEN 'generic'    THEN 'Item'
    WHEN 'movie'      THEN 'Movie'
    WHEN 'tv_show'    THEN 'TV show'
    WHEN 'motorcycle' THEN 'Motorcycle'
    WHEN 'car'        THEN 'Car'
    WHEN 'camera'     THEN 'Camera'
    ELSE kind
  END
$$;

-- "<name> · <Kind label>", the base name truncated so the whole name stays within 200 characters.
CREATE FUNCTION f007_piece_name(base text, kind text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT left(base, 200 - char_length(' · ' || f007_kind_label(kind))) || ' · ' || f007_kind_label(kind)
$$;

-- Longest prefix of s that fits in max_bytes UTF-8 bytes without cutting a character.
CREATE FUNCTION f007_trunc_utf8(s text, max_bytes int) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
  b bytea := convert_to(s, 'UTF8');
  n int := max_bytes;
BEGIN
  IF n <= 0 THEN RETURN ''; END IF;
  IF octet_length(b) <= n THEN RETURN s; END IF;
  LOOP
    BEGIN
      RETURN convert_from(substring(b FROM 1 FOR n), 'UTF8');
    EXCEPTION WHEN SQLSTATE '22021' THEN
      n := n - 1;
      IF n <= 0 THEN RETURN ''; END IF;
    END;
  END LOOP;
END
$$;

-- 1. Split lists that mix kinds: one list per kind in the same folder. The original row keeps the
--    first kind (alphabetical). Items of trashed lists count too.
CREATE TEMP TABLE f007_list_kinds AS
SELECT d.list_id, d.kind,
       row_number() OVER (PARTITION BY d.list_id ORDER BY d.kind) AS rn,
       count(*)     OVER (PARTITION BY d.list_id)                 AS nkinds
FROM (SELECT DISTINCT list_id, kind FROM items) d;

-- The new pieces. When the original list is the root of its trash batch, each piece gets its own
-- batch (same snapshot), so restore and purge of a piece find it as a batch root.
CREATE TEMP TABLE f007_split AS
SELECT lk.list_id, lk.kind,
       gen_random_uuid() AS new_id,
       CASE WHEN b.id IS NOT NULL THEN gen_random_uuid() END AS new_batch_id,
       row_number() OVER (PARTITION BY l.workspace_id, l.folder_id ORDER BY l.id, lk.rn) - 1 AS seq
FROM f007_list_kinds lk
JOIN lists l ON l.id = lk.list_id
LEFT JOIN trash_batches b ON b.id = l.trash_batch_id AND b.root_type = 'list' AND b.root_id = l.id
WHERE lk.rn > 1;

INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
SELECT s.new_batch_id, b.workspace_id, 'list', s.new_id, f007_piece_name(l.name, s.kind), b.former_path, b.deleted_at
FROM f007_split s
JOIN lists l ON l.id = s.list_id
JOIN trash_batches b ON b.id = l.trash_batch_id
WHERE s.new_batch_id IS NOT NULL;

INSERT INTO lists (id, workspace_id, folder_id, name, description, display_mode, sort_mode, sort_order,
                   version, created_at, updated_at, deleted_at, trash_batch_id)
SELECT s.new_id, l.workspace_id, l.folder_id, f007_piece_name(l.name, s.kind), l.description, l.display_mode, l.sort_mode,
       (SELECT COALESCE(max(x.sort_order) + 1, 0) FROM lists x
         WHERE x.workspace_id = l.workspace_id AND x.folder_id IS NOT DISTINCT FROM l.folder_id) + s.seq,
       1, now(), now(), l.deleted_at, COALESCE(s.new_batch_id, l.trash_batch_id)
FROM f007_split s
JOIN lists l ON l.id = s.list_id;

-- Items move to the piece of their kind (sort_order kept). Items trashed with the list's own batch follow the piece's batch.
UPDATE items i
SET list_id = s.new_id,
    trash_batch_id = CASE WHEN s.new_batch_id IS NOT NULL AND i.trash_batch_id = l.trash_batch_id
                          THEN s.new_batch_id ELSE i.trash_batch_id END
FROM f007_split s
JOIN lists l ON l.id = s.list_id
WHERE i.list_id = s.list_id AND i.kind = s.kind;

-- The original rows: snapshot name of their own batch, then the new name.
UPDATE trash_batches b
SET root_name = f007_piece_name(l.name, lk.kind)
FROM f007_list_kinds lk
JOIN lists l ON l.id = lk.list_id
WHERE lk.rn = 1 AND lk.nkinds > 1 AND b.id = l.trash_batch_id AND b.root_type = 'list' AND b.root_id = l.id;

UPDATE lists l
SET name = f007_piece_name(l.name, lk.kind), version = l.version + 1, updated_at = now()
FROM f007_list_kinds lk
WHERE lk.list_id = l.id AND lk.rn = 1 AND lk.nkinds > 1;

-- 2. lists.kind: the kind of the list's items, 'generic' when it has none.
ALTER TABLE lists ADD COLUMN kind TEXT NULL;
UPDATE lists l SET kind = COALESCE((SELECT min(i.kind) FROM items i WHERE i.list_id = l.id), 'generic');
ALTER TABLE lists ALTER COLUMN kind SET NOT NULL;
ALTER TABLE lists ADD CONSTRAINT lists_kind_check CHECK (kind ~ '^[a-z][a-z0-9_]{0,31}$');

ALTER TABLE lists DROP CONSTRAINT lists_description_check;
ALTER TABLE lists ADD CONSTRAINT lists_description_check CHECK (char_length(description) <= 65536);
ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (sort_mode IN ('manual', 'title', 'updated', 'kind_key'));

-- 3. Move summary, status and the dropped metadata keys into the Markdown content.
CREATE TEMP TABLE f007_kept (kind text PRIMARY KEY, keys text[] NOT NULL);
INSERT INTO f007_kept (kind, keys) VALUES
  ('generic',    '{}'),
  ('movie',      '{release_year,runtime_minutes,imdb_url,streaming}'),
  ('tv_show',    '{first_air_year,end_year,seasons,imdb_url,streaming}'),
  ('motorcycle', '{make,model,model_year,displacement_cc,power_hp,license_class}'),
  ('car',        '{make,model,model_year,fuel_type,power_hp}'),
  ('camera',     '{make,model,release_year,sensor_format,megapixels}');

CREATE TEMP TABLE f007_move AS
SELECT i.id AS item_id, b.bullets
FROM items i
LEFT JOIN f007_kept k ON k.kind = i.kind
CROSS JOIN LATERAL (
  SELECT concat_ws(E'\n',
    CASE WHEN nullif(i.summary, '') IS NOT NULL THEN '- **Summary:** ' || replace(i.summary, E'\n', E'\n  ') END,
    CASE WHEN nullif(i.status, '')  IS NOT NULL THEN '- **Status:** '  || replace(i.status,  E'\n', E'\n  ') END,
    (SELECT string_agg(
        '- **' || upper(left(replace(e.key, '_', ' '), 1)) || substr(replace(e.key, '_', ' '), 2) || ':** ' ||
        replace(CASE WHEN jsonb_typeof(e.value) = 'string' THEN e.value #>> '{}' ELSE e.value::text END, E'\n', E'\n  '),
        E'\n' ORDER BY e.key)
       FROM jsonb_each(i.metadata) e
      WHERE NOT (e.key = ANY (COALESCE(k.keys, '{}'))))
  ) AS bullets
) b
WHERE b.bullets <> '';

-- A new content version per touched item; skipped when the latest body already starts with the block.
INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at)
SELECT m.item_id, COALESCE(lv.version, 0) + 1, nb.body, octet_length(nb.body), sha256(convert_to(nb.body, 'UTF8')), now()
FROM f007_move m
LEFT JOIN LATERAL (
  SELECT v.version, v.body FROM item_content_versions v WHERE v.item_id = m.item_id ORDER BY v.version DESC LIMIT 1
) lv ON true
CROSS JOIN LATERAL (SELECT '## From 1.0' || E'\n\n' || m.bullets || E'\n\n---' AS head) h
CROSS JOIN LATERAL (
  -- over 1 MiB the previous body is cut to fit (the block itself is always kept)
  SELECT h.head || CASE WHEN COALESCE(lv.body, '') = '' THEN E'\n'
                        ELSE E'\n\n' || f007_trunc_utf8(lv.body, 1048576 - octet_length(h.head) - 2) END AS body
) nb
WHERE lv.body IS NULL OR left(lv.body, 11) <> '## From 1.0';

-- Metadata keeps only the kept keys; touched items get a new version.
UPDATE items i
SET metadata = COALESCE((SELECT jsonb_object_agg(e.key, e.value) FROM jsonb_each(i.metadata) e
                          WHERE e.key = ANY (COALESCE(k.keys, '{}'))), '{}'::jsonb),
    version = i.version + 1,
    updated_at = now()
FROM f007_move m
LEFT JOIN f007_kept k ON k.kind = (SELECT x.kind FROM items x WHERE x.id = m.item_id)
WHERE i.id = m.item_id;

-- 4. Drops and rename.
DROP INDEX items_active_kind_idx;
ALTER TABLE items DROP COLUMN kind;
ALTER TABLE items DROP COLUMN summary;
ALTER TABLE items DROP COLUMN status;
ALTER TABLE items RENAME COLUMN official_website_url TO website_url;
ALTER TABLE items RENAME CONSTRAINT items_official_website_url_check TO items_website_url_check;

ALTER TABLE workspace_settings DROP COLUMN default_currency;

DROP TABLE f007_move;
DROP TABLE f007_kept;
DROP TABLE f007_split;
DROP TABLE f007_list_kinds;
DROP FUNCTION f007_trunc_utf8(text, int);
DROP FUNCTION f007_piece_name(text, text);
DROP FUNCTION f007_kind_label(text);
