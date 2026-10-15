-- F002-B4: list sort mode 'price' (price_min ascending, no price last). No index: a list is read whole per page at this scale.
ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (sort_mode IN ('manual', 'title', 'updated', 'kind_key', 'price'));
