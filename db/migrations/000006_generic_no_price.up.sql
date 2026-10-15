-- F008-B1 generic has no price (API 0.6.0). A generic list has no fields at all, so the price of its items
-- is moved into the item's Markdown content (a new version starting with a "From 1.2" block) before the
-- price columns are cleared: nothing is lost. `price` also leaves the pinned fields of generic lists, and a
-- price sort falls back to manual.

-- Helper, dropped at the end of this file. Longest prefix of s that fits in max_bytes UTF-8 bytes
-- without cutting a character.
CREATE FUNCTION f008_trunc_utf8(s text, max_bytes int) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
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

-- Items (trashed ones too) of generic lists that carry a price. Text: "EUR 1200.00" (single price),
-- "EUR 1000.00–1400.00" (range), "EUR 1000.00" (only a minimum), "EUR up to 1400.00" (only a maximum).
CREATE TEMP TABLE f008_move AS
SELECT i.id AS item_id,
  '- **Price:** ' || concat_ws(' ', i.currency,
    CASE
      WHEN i.price_min IS NOT NULL AND i.price_max IS NOT NULL AND i.price_min <> i.price_max THEN i.price_min::text || '–' || i.price_max::text
      WHEN i.price_min IS NOT NULL THEN i.price_min::text
      ELSE 'up to ' || i.price_max::text
    END) AS bullets
FROM items i
JOIN lists l ON l.id = i.list_id AND l.kind = 'generic'
WHERE i.price_min IS NOT NULL OR i.price_max IS NOT NULL;

-- A new content version per touched item; skipped when the latest body already starts with the block.
INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256, created_at)
SELECT m.item_id, COALESCE(lv.version, 0) + 1, nb.body, octet_length(nb.body), sha256(convert_to(nb.body, 'UTF8')), now()
FROM f008_move m
LEFT JOIN LATERAL (
  SELECT v.version, v.body FROM item_content_versions v WHERE v.item_id = m.item_id ORDER BY v.version DESC LIMIT 1
) lv ON true
CROSS JOIN LATERAL (SELECT '## From 1.2' || E'\n\n' || m.bullets || E'\n\n---' AS head) h
CROSS JOIN LATERAL (
  -- over 1 MiB the previous body is cut to fit (the block itself is always kept)
  SELECT h.head || CASE WHEN COALESCE(lv.body, '') = '' THEN E'\n'
                        ELSE E'\n\n' || f008_trunc_utf8(lv.body, 1048576 - octet_length(h.head) - 2) END AS body
) nb
WHERE lv.body IS NULL OR left(lv.body, 11) <> '## From 1.2';

-- The price leaves the item; touched items get a new version.
UPDATE items i
SET price_min = NULL, price_max = NULL, currency = NULL,
    version = i.version + 1,
    updated_at = now()
FROM f008_move m
WHERE i.id = m.item_id;

-- Generic lists: no pinned `price`, no price sort.
UPDATE lists
SET pinned_fields = jsonb_build_object(
      'under', COALESCE((SELECT jsonb_agg(e) FROM jsonb_array_elements(COALESCE(pinned_fields -> 'under', '[]'::jsonb)) e WHERE e <> '"price"'::jsonb), '[]'::jsonb),
      'right', COALESCE((SELECT jsonb_agg(e) FROM jsonb_array_elements(COALESCE(pinned_fields -> 'right', '[]'::jsonb)) e WHERE e <> '"price"'::jsonb), '[]'::jsonb)),
    version = version + 1,
    updated_at = now()
WHERE kind = 'generic' AND pinned_fields IS NOT NULL
  AND ((pinned_fields -> 'under') @> '"price"'::jsonb OR (pinned_fields -> 'right') @> '"price"'::jsonb);

UPDATE lists
SET sort_mode = 'manual', version = version + 1, updated_at = now()
WHERE kind = 'generic' AND sort_mode = 'price';

DROP TABLE f008_move;
DROP FUNCTION f008_trunc_utf8(text, int);
