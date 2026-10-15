-- LOSSY by nature: restores the 1.0 columns and constraints, not the data. Summary and status come back
-- NULL, dropped metadata keys stay in the Markdown "From 1.0" blocks, split lists stay split, and the
-- default currency goes back to EUR. Take a backup first (BACKUP_AND_DEPLOYMENT §4).

ALTER TABLE workspace_settings ADD COLUMN default_currency CHAR(3) NOT NULL DEFAULT 'EUR';
ALTER TABLE workspace_settings ADD CONSTRAINT workspace_settings_default_currency_check CHECK (default_currency ~ '^[A-Z]{3}$');

ALTER TABLE items RENAME CONSTRAINT items_website_url_check TO items_official_website_url_check;
ALTER TABLE items RENAME COLUMN website_url TO official_website_url;

ALTER TABLE items ADD COLUMN kind TEXT NOT NULL DEFAULT 'generic';
UPDATE items i SET kind = l.kind FROM lists l WHERE l.id = i.list_id AND l.workspace_id = i.workspace_id;
ALTER TABLE items ADD COLUMN summary TEXT NULL;
ALTER TABLE items ADD COLUMN status TEXT NULL;
ALTER TABLE items ADD CONSTRAINT items_kind_check CHECK (kind ~ '^[a-z][a-z0-9_]{0,31}$');
ALTER TABLE items ADD CONSTRAINT items_summary_check CHECK (char_length(summary) <= 1000);
ALTER TABLE items ADD CONSTRAINT items_status_check CHECK (char_length(status) <= 100);
CREATE INDEX items_active_kind_idx ON items (workspace_id, kind) WHERE deleted_at IS NULL;

UPDATE lists SET sort_mode = 'manual' WHERE sort_mode = 'kind_key';
ALTER TABLE lists DROP CONSTRAINT lists_sort_mode_check;
ALTER TABLE lists ADD CONSTRAINT lists_sort_mode_check CHECK (sort_mode IN ('manual', 'title', 'updated'));

UPDATE lists SET description = left(description, 2000) WHERE char_length(description) > 2000;
ALTER TABLE lists DROP CONSTRAINT lists_description_check;
ALTER TABLE lists ADD CONSTRAINT lists_description_check CHECK (char_length(description) <= 2000);

ALTER TABLE lists DROP CONSTRAINT lists_kind_check;
ALTER TABLE lists DROP COLUMN kind;
