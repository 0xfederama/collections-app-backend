-- F012-B3 trash auto-purge (API 0.8.0). `trash_retention_days` is the number of days a trash batch is kept before the
-- daily job purges it; NULL (the default) turns the job off. Existing workspaces stay NULL.
ALTER TABLE workspace_settings ADD COLUMN trash_retention_days integer NULL;
ALTER TABLE workspace_settings ADD CONSTRAINT workspace_settings_trash_retention_days_check CHECK (
  trash_retention_days IS NULL OR trash_retention_days BETWEEN 1 AND 365
);
