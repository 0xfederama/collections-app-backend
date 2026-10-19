-- F-016 (API 0.12.0): a list pins up to three fields, on every type. Lists have no
-- CHECK (the limit is validated in Go), so only the type default needs a new CHECK.
ALTER TABLE type_definitions DROP CONSTRAINT type_definitions_default_pinned_fields_check;
ALTER TABLE type_definitions ADD CONSTRAINT type_definitions_default_pinned_fields_check CHECK (
  default_pinned_fields IS NULL OR
  (jsonb_typeof(default_pinned_fields) = 'array' AND jsonb_array_length(default_pinned_fields) <= 3)
);
