DROP FUNCTION seed_builtin_kind_definitions(uuid);
ALTER FUNCTION seed_builtin_kind_definitions_v1(uuid) RENAME TO seed_builtin_kind_definitions;
UPDATE kind_definitions SET default_pinned_fields = NULL WHERE is_locked;
DROP FUNCTION apply_builtin_kind_default_pins(uuid);
