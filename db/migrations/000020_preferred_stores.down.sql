-- LOSSY: the preferred stores setting is dropped. The field type CHECK is restored
-- without `stores`, so this fails (and changes nothing) while a stores field exists:
-- the ADD CONSTRAINT validates the existing rows. Down the book type first.
ALTER TABLE type_fields DROP CONSTRAINT type_fields_field_type_check;
ALTER TABLE type_fields ADD CONSTRAINT type_fields_field_type_check CHECK (
  field_type IN ('text', 'number', 'date', 'boolean', 'link', 'streaming')
);

ALTER TABLE workspace_settings
  DROP CONSTRAINT workspace_settings_preferred_stores_check,
  DROP COLUMN preferred_stores;
