-- Restore the prior fixed token for lists on their kind's default field.
UPDATE lists l
SET sort_mode = 'kind_key',
    version = l.version + 1,
    updated_at = now()
FROM kind_definitions k
WHERE l.workspace_id = k.workspace_id
  AND l.kind = k.key
  AND k.default_sort_field IS NOT NULL
  AND l.sort_mode = k.default_sort_field;
