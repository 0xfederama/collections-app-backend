-- F009-B2 pinned fields are ordered (API 0.7.0). `lists.pinned_fields` stops being the F-002
-- `{"under":[key],"right":[key]}` slots and becomes an ordered JSON array of at most two keys: the first is the
-- primary field, the second the secondary. Existing pins are carried over in order: the old `under` keys come
-- first, then the old `right` keys, duplicates are dropped and only the first two survive (the format allows
-- at most two from now on).

-- Helper, dropped at the end of this file. Ordered, de-duplicated, at most two keys of the F-002 slots.
CREATE FUNCTION f009_ordered_pins(p jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
  SELECT COALESCE(jsonb_agg(k ORDER BY o), '[]'::jsonb)
  FROM (
    SELECT k, min(o) AS o
    FROM (
      SELECT value AS k, ordinality AS o
        FROM jsonb_array_elements_text(COALESCE(p -> 'under', '[]'::jsonb)) WITH ORDINALITY
      UNION ALL
      SELECT value, 1000 + ordinality
        FROM jsonb_array_elements_text(COALESCE(p -> 'right', '[]'::jsonb)) WITH ORDINALITY
    ) e
    GROUP BY k
    ORDER BY min(o)
    LIMIT 2
  ) s;
$$;

UPDATE lists
SET pinned_fields = f009_ordered_pins(pinned_fields),
    version = version + 1,
    updated_at = now()
WHERE pinned_fields IS NOT NULL AND jsonb_typeof(pinned_fields) = 'object';

DROP FUNCTION f009_ordered_pins(jsonb);
