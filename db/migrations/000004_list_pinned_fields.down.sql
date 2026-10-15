-- Drops the pinned fields of every list (they are a display preference).
ALTER TABLE lists DROP COLUMN pinned_fields;
