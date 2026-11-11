DROP TABLE IF EXISTS folder_members;
DROP TABLE IF EXISTS user_top_order;
DROP INDEX IF EXISTS lists_owner_idx;
ALTER TABLE lists DROP CONSTRAINT IF EXISTS lists_owner_fk;
ALTER TABLE lists DROP COLUMN IF EXISTS owner_user_id;
DROP INDEX IF EXISTS folders_owner_idx;
ALTER TABLE folders DROP CONSTRAINT IF EXISTS folders_owner_fk;
ALTER TABLE folders DROP COLUMN IF EXISTS owner_user_id;
DROP TABLE IF EXISTS users;
