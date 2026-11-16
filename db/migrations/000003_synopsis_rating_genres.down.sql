ALTER TABLE item_content_versions DROP CONSTRAINT IF EXISTS item_content_versions_origin_check;
ALTER TABLE item_content_versions DROP COLUMN IF EXISTS origin;
ALTER TABLE lookup_imports DROP COLUMN IF EXISTS import_generation;
ALTER TABLE lookup_imports DROP COLUMN IF EXISTS description_sha256, DROP COLUMN IF EXISTS description_version;
