-- Drops the list icon columns (F012-B1). Lossy: the icons are gone.
ALTER TABLE lists DROP CONSTRAINT lists_icon_color_check;
ALTER TABLE lists DROP COLUMN icon_color;
ALTER TABLE lists DROP COLUMN icon;
