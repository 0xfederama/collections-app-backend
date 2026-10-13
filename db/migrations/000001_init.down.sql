-- DESTRUCTIVE: drops every table created by 000001 and all the data in them (DATA_MODEL §8).
-- Take a backup first (BACKUP_AND_DEPLOYMENT §4). Files under DATA_DIR are not touched.

DROP TABLE item_contents;
DROP TABLE item_content_versions;
DROP TABLE items;
DROP TABLE attachments;
DROP TABLE lists;
DROP TABLE folders;
DROP TABLE trash_batches;
DROP TABLE workspace_settings;
DROP TABLE workspaces;

DROP EXTENSION IF EXISTS pg_trgm;
