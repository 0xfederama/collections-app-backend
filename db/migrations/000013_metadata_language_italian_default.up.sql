-- New workspaces should start TMDB lookups in Italian. Existing choices stay unchanged.
ALTER TABLE workspace_settings ALTER COLUMN metadata_language SET DEFAULT 'it-IT';
