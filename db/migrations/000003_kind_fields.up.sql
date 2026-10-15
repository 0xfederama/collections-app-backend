-- F-002 kind fields (API 0.4.0): motorcycle and camera lose `make` and `model`.
-- Their values are moved into the item's Markdown content (a new version starting with a "From 1.1"
-- block) before the keys are removed from the metadata: nothing is lost.

-- Helper, dropped at the end of this file. Longest prefix of s that fits in max_bytes UTF-8 bytes
-- without cutting a character.
CREATE FUNCTION f002_trunc_utf8(s text, max_bytes int) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
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

-- Items (trashed ones too) of motorcycle and camera lists that carry a make or a model.
CREATE TEMP TABLE f002_move AS
SELECT i.id AS item_id, b.bullets
FROM items i
JOIN lists l ON l.id = i.list_id AND l.kind IN ('motorcycle', 'camera')
CROSS JOIN LATERAL (
  SELECT concat_ws(E'\n',
    CASE WHEN nullif(i.metadata ->> 'make',  '') IS NOT NULL THEN '- **Make:** '  || replace(i.metadata ->> 'make',  E'\n', E'\n  ') END,
    CASE WHEN nullif(i.metadata ->> 'model', '') IS NOT NULL THEN '- **Model:** ' || replace(i.metadata ->> 'model', E'\n', E'\n  ') END
  ) AS bullets
) b
WHERE b.bullets <> '';

-- A new content version per touched item; skipped when the latest body already starts with the block.
INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at)
SELECT m.item_id, COALESCE(lv.version, 0) + 1, nb.body, octet_length(nb.body), sha256(convert_to(nb.body, 'UTF8')), now()
FROM f002_move m
LEFT JOIN LATERAL (
  SELECT v.version, v.body FROM item_content_versions v WHERE v.item_id = m.item_id ORDER BY v.version DESC LIMIT 1
) lv ON true
CROSS JOIN LATERAL (SELECT '## From 1.1' || E'\n\n' || m.bullets || E'\n\n---' AS head) h
CROSS JOIN LATERAL (
  -- over 1 MiB the previous body is cut to fit (the block itself is always kept)
  SELECT h.head || CASE WHEN COALESCE(lv.body, '') = '' THEN E'\n'
                        ELSE E'\n\n' || f002_trunc_utf8(lv.body, 1048576 - octet_length(h.head) - 2) END AS body
) nb
WHERE lv.body IS NULL OR left(lv.body, 11) <> '## From 1.1';

-- The keys leave the metadata; touched items get a new version.
UPDATE items i
SET metadata = (i.metadata - 'make') - 'model',
    version = i.version + 1,
    updated_at = now()
FROM f002_move m
WHERE i.id = m.item_id;

-- Items with a make or model that is empty (or not text) are not in f002_move; clean their keys too.
UPDATE items i
SET metadata = (i.metadata - 'make') - 'model',
    version = i.version + 1,
    updated_at = now()
FROM lists l
WHERE l.id = i.list_id AND l.kind IN ('motorcycle', 'camera')
  AND (i.metadata ? 'make' OR i.metadata ? 'model');

DROP TABLE f002_move;
DROP FUNCTION f002_trunc_utf8(text, int);
