-- Drops the trash retention setting (F012-B3). Lossy: the chosen retention is gone.
ALTER TABLE workspace_settings DROP CONSTRAINT workspace_settings_trash_retention_days_check;
ALTER TABLE workspace_settings DROP COLUMN trash_retention_days;
