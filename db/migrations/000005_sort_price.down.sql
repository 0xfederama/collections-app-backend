UPDATE lists SET sort_mode = 'manual' WHERE sort_mode = 'price';
ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (sort_mode IN ('manual', 'title', 'updated', 'kind_key'));
