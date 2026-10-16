-- LOSSY by design: the ordered list has no left/right slot information, so every key goes back into `under`
-- and `right` is empty. The order of the pins is kept. From API 0.7.0 on the API only writes the ordered
-- array, so this down is for a rollback only.
UPDATE lists
SET pinned_fields = jsonb_build_object('under', pinned_fields, 'right', '[]'::jsonb),
    version = version + 1,
    updated_at = now()
WHERE pinned_fields IS NOT NULL AND jsonb_typeof(pinned_fields) = 'array';
