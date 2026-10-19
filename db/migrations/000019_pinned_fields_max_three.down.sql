-- LOSSY by design: the third pin of a list or a type default is dropped, keeping the
-- first two (the old limit). Rows bump their version so clients refetch them.
UPDATE type_definitions
SET default_pinned_fields = (
      SELECT jsonb_agg(p.e ORDER BY p.n)
      FROM (
        SELECT e, n FROM jsonb_array_elements(default_pinned_fields) WITH ORDINALITY AS t(e, n)
        ORDER BY n LIMIT 2
      ) p
    ),
    version = version + 1,
    updated_at = now()
WHERE jsonb_typeof(default_pinned_fields) = 'array' AND jsonb_array_length(default_pinned_fields) > 2;

UPDATE lists
SET pinned_fields = (
      SELECT jsonb_agg(p.e ORDER BY p.n)
      FROM (
        SELECT e, n FROM jsonb_array_elements(pinned_fields) WITH ORDINALITY AS t(e, n)
        ORDER BY n LIMIT 2
      ) p
    ),
    version = version + 1,
    updated_at = now()
WHERE jsonb_typeof(pinned_fields) = 'array' AND jsonb_array_length(pinned_fields) > 2;

ALTER TABLE type_definitions DROP CONSTRAINT type_definitions_default_pinned_fields_check;
ALTER TABLE type_definitions ADD CONSTRAINT type_definitions_default_pinned_fields_check CHECK (
  default_pinned_fields IS NULL OR
  (jsonb_typeof(default_pinned_fields) = 'array' AND jsonb_array_length(default_pinned_fields) <= 2)
);
