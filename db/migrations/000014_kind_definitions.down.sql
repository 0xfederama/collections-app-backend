-- LOSSY for custom definitions: pre-1.6.0 cannot represent custom kinds or
-- fields. Built-in list kinds and all item metadata remain unchanged.
DROP TRIGGER workspaces_seed_builtin_kind_definitions ON workspaces;
DROP FUNCTION seed_builtin_kind_definitions_on_workspace_insert();
DROP FUNCTION seed_builtin_kind_definitions(uuid);
DROP TABLE kind_fields;
DROP TABLE kind_definitions;
