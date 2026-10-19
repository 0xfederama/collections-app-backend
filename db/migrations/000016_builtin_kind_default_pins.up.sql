-- F-014 follow-up: built-in kinds carry their default pinned fields again
-- (first pin next to the title, second in the right corner). Locked kinds are
-- protected by the service, not by the database, so this UPDATE is allowed.
-- The seed function is wrapped so fresh workspaces receive the same values.
ALTER FUNCTION seed_builtin_kind_definitions(uuid) RENAME TO seed_builtin_kind_definitions_v1;

CREATE FUNCTION apply_builtin_kind_default_pins(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE kind_definitions k
  SET default_pinned_fields = s.pins::jsonb
  FROM (
  VALUES
    ('movie', '["runtime_minutes","streaming"]'),
    ('tv_show', '["seasons","streaming"]'),
    ('motorcycle', '["displacement_cc","price"]'),
    ('car', '["model_year","price"]'),
    ('camera', '["camera_type","price"]'),
    ('generic', '[]')
  ) AS s(key, pins)
  WHERE k.workspace_id = target_workspace_id
    AND k.key = s.key
    AND k.is_locked
    AND k.default_pinned_fields IS NULL;
END;
$$;

CREATE FUNCTION seed_builtin_kind_definitions(target_workspace_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM seed_builtin_kind_definitions_v1(target_workspace_id);
  PERFORM apply_builtin_kind_default_pins(target_workspace_id);
END;
$$;

SELECT apply_builtin_kind_default_pins(id) FROM workspaces;
