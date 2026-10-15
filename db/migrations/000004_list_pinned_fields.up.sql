-- F002-B3: which fields a list pins in its rows. NULL = the kind default (the web owns the defaults).
-- JSON object {"under": [key...], "right": [key...]}, validated in Go (internal/items/kinds), so no CHECK.
ALTER TABLE lists ADD COLUMN pinned_fields JSONB NULL;
