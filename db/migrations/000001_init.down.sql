DROP TRIGGER IF EXISTS workspaces_seed_builtin_type_definitions ON workspaces;

DROP FUNCTION IF EXISTS seed_builtin_type_definitions_on_workspace_insert();
DROP FUNCTION IF EXISTS seed_builtin_type_definitions(uuid);
DROP FUNCTION IF EXISTS apply_builtin_type_default_pins(uuid);

DROP TABLE IF EXISTS lookup_imports;
DROP TABLE IF EXISTS item_content_versions;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS lists;
DROP TABLE IF EXISTS folders;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS trash_batches;
DROP TABLE IF EXISTS type_fields;
DROP TABLE IF EXISTS type_definitions;
DROP TABLE IF EXISTS workspace_settings;
DROP TABLE IF EXISTS workspaces;

DROP EXTENSION IF EXISTS pg_trgm;
