-- F-016 (API 0.12.0, D-096): the `stores` field type and the preferred stores setting.
-- `stores` joins the field type CHECK for locked fields only, like `streaming`: the
-- custom-field CHECK (000014, renamed in 000017) is unchanged, so no custom field
-- can be a stores field. Values are arrays of provider keys (see internal/stores).
ALTER TABLE type_fields DROP CONSTRAINT type_fields_field_type_check;
ALTER TABLE type_fields ADD CONSTRAINT type_fields_field_type_check CHECK (
  field_type IN ('text', 'number', 'date', 'boolean', 'link', 'streaming', 'stores')
);

-- The per-element check lists the six keys (a CHECK cannot hold a subquery). Uniqueness
-- is enforced by the service, not here.
ALTER TABLE workspace_settings
  ADD COLUMN preferred_stores TEXT[] NOT NULL DEFAULT '{}',
  ADD CONSTRAINT workspace_settings_preferred_stores_check CHECK (
    preferred_stores <@ ARRAY['openlibrary', 'bookshop', 'amazon', 'apple_books', 'google_play_books', 'kobo']::text[]
  );
