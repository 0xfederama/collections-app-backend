-- F014-B4 replaces the fixed kind_key token with its definition-backed field key.
-- Field keys are validated against the workspace definitions in the service;
-- the old static CHECK cannot represent custom keys.
ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
UPDATE lists l
SET sort_mode = COALESCE(k.default_sort_field, 'manual'),
    version = l.version + 1,
    updated_at = now()
FROM kind_definitions k
WHERE l.workspace_id = k.workspace_id
  AND l.kind = k.key
  AND l.sort_mode = 'kind_key';
