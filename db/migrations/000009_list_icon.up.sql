-- F012-B1 list icon (API 0.8.0). `icon` is an emoji (one grapheme) or `i:<name>` for a built-in icon key; NULL is the
-- default. `icon_color` is one of the ten palette names and only applies to a built-in icon: it must be NULL unless
-- `icon` starts with `i:`. The service also clears it when the icon changes to an emoji or to NULL. Existing lists stay NULL.
ALTER TABLE lists ADD COLUMN icon text NULL;
ALTER TABLE lists ADD COLUMN icon_color text NULL;
ALTER TABLE lists ADD CONSTRAINT lists_icon_color_check CHECK (
  icon_color IS NULL
  OR (icon IS NOT NULL AND icon LIKE 'i:%' AND icon_color IN ('red', 'orange', 'amber', 'green', 'teal', 'blue', 'indigo', 'purple', 'pink', 'gray'))
);
