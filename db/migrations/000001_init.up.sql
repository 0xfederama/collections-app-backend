-- Initial schema (DATA_MODEL §2).
-- Constraints and indexes are named <table>_<column(s)>_{pkey,key,fkey,check,idx} so later tasks can map them to domain errors.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE workspaces (
  id          UUID        NOT NULL DEFAULT gen_random_uuid(),
  name        TEXT        NOT NULL,
  version     INTEGER     NOT NULL DEFAULT 1,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT workspaces_pkey PRIMARY KEY (id),
  CONSTRAINT workspaces_name_check CHECK (char_length(name) BETWEEN 1 AND 200)
);

CREATE TABLE workspace_settings (
  workspace_id           UUID        NOT NULL,
  streaming_country_code CHAR(2)     NOT NULL DEFAULT 'IT',
  default_currency       CHAR(3)     NOT NULL DEFAULT 'EUR',
  markdown_history_limit INTEGER     NOT NULL DEFAULT 20,
  version                INTEGER     NOT NULL DEFAULT 1,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT workspace_settings_pkey PRIMARY KEY (workspace_id),
  CONSTRAINT workspace_settings_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT workspace_settings_streaming_country_code_check CHECK (streaming_country_code ~ '^[A-Z]{2}$'),
  CONSTRAINT workspace_settings_default_currency_check CHECK (default_currency ~ '^[A-Z]{3}$'),
  CONSTRAINT workspace_settings_markdown_history_limit_check CHECK (markdown_history_limit BETWEEN 0 AND 200)
);

-- One row per user-initiated delete. The Trash UI lists these rows.
CREATE TABLE trash_batches (
  id            UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id  UUID        NOT NULL,
  root_type     TEXT        NOT NULL,
  root_id       UUID        NOT NULL, -- polymorphic; integrity maintained by the trash service
  root_name     TEXT        NOT NULL, -- snapshot for display
  former_path   JSONB       NOT NULL, -- [{"type":"folder","id":"…","name":"…"}, …] snapshot at delete time
  deleted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT trash_batches_pkey PRIMARY KEY (id),
  CONSTRAINT trash_batches_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT trash_batches_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT trash_batches_root_type_check CHECK (root_type IN ('folder', 'list', 'item'))
);
CREATE INDEX trash_batches_workspace_deleted_idx ON trash_batches (workspace_id, deleted_at DESC);

CREATE TABLE folders (
  id               UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id     UUID        NOT NULL,
  parent_folder_id UUID        NULL,
  name             TEXT        NOT NULL,
  sort_order       INTEGER     NOT NULL,
  version          INTEGER     NOT NULL DEFAULT 1,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at       TIMESTAMPTZ NULL,
  trash_batch_id   UUID        NULL, -- no ON DELETE action (D-051)
  CONSTRAINT folders_pkey PRIMARY KEY (id),
  CONSTRAINT folders_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT folders_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT folders_parent_fkey FOREIGN KEY (parent_folder_id, workspace_id) REFERENCES folders (id, workspace_id) ON DELETE CASCADE,
  CONSTRAINT folders_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES trash_batches (id),
  CONSTRAINT folders_name_check CHECK (char_length(name) BETWEEN 1 AND 200),
  CONSTRAINT folders_no_self_parent_check CHECK (parent_folder_id IS DISTINCT FROM id),
  CONSTRAINT folders_trash_check CHECK ((deleted_at IS NULL) = (trash_batch_id IS NULL))
);
CREATE INDEX folders_active_parent_idx ON folders (workspace_id, parent_folder_id, sort_order) WHERE deleted_at IS NULL;
CREATE INDEX folders_trash_batch_idx ON folders (trash_batch_id) WHERE trash_batch_id IS NOT NULL;

CREATE TABLE lists (
  id             UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id   UUID        NOT NULL,
  folder_id      UUID        NULL,
  name           TEXT        NOT NULL,
  description    TEXT        NULL,
  display_mode   TEXT        NOT NULL DEFAULT 'thumbnail',
  sort_mode      TEXT        NOT NULL DEFAULT 'manual',
  sort_order     INTEGER     NOT NULL,
  version        INTEGER     NOT NULL DEFAULT 1,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at     TIMESTAMPTZ NULL,
  trash_batch_id UUID        NULL, -- no ON DELETE action (D-051)
  CONSTRAINT lists_pkey PRIMARY KEY (id),
  CONSTRAINT lists_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT lists_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT lists_folder_fkey FOREIGN KEY (folder_id, workspace_id) REFERENCES folders (id, workspace_id) ON DELETE CASCADE,
  CONSTRAINT lists_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES trash_batches (id),
  CONSTRAINT lists_name_check CHECK (char_length(name) BETWEEN 1 AND 200),
  CONSTRAINT lists_description_check CHECK (char_length(description) <= 2000),
  CONSTRAINT lists_display_mode_check CHECK (display_mode IN ('compact', 'thumbnail', 'grid')),
  CONSTRAINT lists_sort_mode_check CHECK (sort_mode IN ('manual', 'title', 'updated')),
  CONSTRAINT lists_trash_check CHECK ((deleted_at IS NULL) = (trash_batch_id IS NULL))
);
CREATE INDEX lists_active_folder_idx ON lists (workspace_id, folder_id, sort_order) WHERE deleted_at IS NULL;
CREATE INDEX lists_trash_batch_idx ON lists (trash_batch_id) WHERE trash_batch_id IS NOT NULL;

-- Independent of items, so there is no circular FK. An item points to its attachment.
CREATE TABLE attachments (
  id                    UUID        NOT NULL DEFAULT gen_random_uuid(),
  workspace_id          UUID        NOT NULL,
  storage_key           TEXT        NOT NULL, -- e.g. images/3f/a2/<uuid>.webp (never user-derived)
  thumbnail_storage_key TEXT        NOT NULL, -- e.g. images/3f/a2/<uuid>.thumb.jpg
  original_filename     TEXT        NULL,     -- display only
  media_type            TEXT        NOT NULL,
  size_bytes            BIGINT      NOT NULL,
  width                 INTEGER     NOT NULL,
  height                INTEGER     NOT NULL,
  sha256                BYTEA       NOT NULL,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT attachments_pkey PRIMARY KEY (id),
  CONSTRAINT attachments_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT attachments_storage_key_key UNIQUE (storage_key),
  CONSTRAINT attachments_thumbnail_storage_key_key UNIQUE (thumbnail_storage_key),
  CONSTRAINT attachments_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT attachments_original_filename_check CHECK (char_length(original_filename) <= 255),
  CONSTRAINT attachments_media_type_check CHECK (media_type IN ('image/jpeg', 'image/png', 'image/webp')),
  CONSTRAINT attachments_size_bytes_check CHECK (size_bytes BETWEEN 1 AND 10485760),
  CONSTRAINT attachments_width_check CHECK (width > 0),
  CONSTRAINT attachments_height_check CHECK (height > 0)
);

CREATE TABLE items (
  id                   UUID          NOT NULL DEFAULT gen_random_uuid(),
  workspace_id         UUID          NOT NULL,
  list_id              UUID          NOT NULL,
  title                TEXT          NOT NULL,
  kind                 TEXT          NOT NULL DEFAULT 'generic',
  summary              TEXT          NULL,
  status               TEXT          NULL,
  image_url            TEXT          NULL,
  image_attachment_id  UUID          NULL,
  price_min            NUMERIC(12,2) NULL,
  price_max            NUMERIC(12,2) NULL,
  currency             CHAR(3)       NULL,
  official_website_url TEXT          NULL,
  metadata             JSONB         NOT NULL DEFAULT '{}',
  sort_order           INTEGER       NOT NULL,
  version              INTEGER       NOT NULL DEFAULT 1, -- "metadata version" (everything except Markdown)
  created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
  deleted_at           TIMESTAMPTZ   NULL,
  trash_batch_id       UUID          NULL, -- no ON DELETE action (D-051)
  CONSTRAINT items_pkey PRIMARY KEY (id),
  CONSTRAINT items_id_workspace_id_key UNIQUE (id, workspace_id),
  CONSTRAINT items_image_attachment_id_key UNIQUE (image_attachment_id),
  CONSTRAINT items_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
  CONSTRAINT items_list_fkey FOREIGN KEY (list_id, workspace_id) REFERENCES lists (id, workspace_id) ON DELETE CASCADE,
  CONSTRAINT items_image_attachment_fkey FOREIGN KEY (image_attachment_id, workspace_id) REFERENCES attachments (id, workspace_id),
  CONSTRAINT items_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES trash_batches (id),
  CONSTRAINT items_title_check CHECK (char_length(title) BETWEEN 1 AND 300),
  CONSTRAINT items_kind_check CHECK (kind ~ '^[a-z][a-z0-9_]{0,31}$'),
  CONSTRAINT items_summary_check CHECK (char_length(summary) <= 1000),
  CONSTRAINT items_status_check CHECK (char_length(status) <= 100),
  CONSTRAINT items_image_url_check CHECK (char_length(image_url) <= 2048),
  CONSTRAINT items_price_min_check CHECK (price_min >= 0),
  CONSTRAINT items_price_max_check CHECK (price_max >= 0),
  CONSTRAINT items_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT items_official_website_url_check CHECK (char_length(official_website_url) <= 2048),
  CONSTRAINT items_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
  CONSTRAINT items_image_source_check CHECK (NOT (image_url IS NOT NULL AND image_attachment_id IS NOT NULL)),
  CONSTRAINT items_price_range_check CHECK (price_min IS NULL OR price_max IS NULL OR price_min <= price_max),
  CONSTRAINT items_price_currency_check CHECK ((price_min IS NULL AND price_max IS NULL) OR currency IS NOT NULL),
  CONSTRAINT items_trash_check CHECK ((deleted_at IS NULL) = (trash_batch_id IS NULL))
);
CREATE INDEX items_active_list_idx ON items (list_id, sort_order) WHERE deleted_at IS NULL;
CREATE INDEX items_active_kind_idx ON items (workspace_id, kind) WHERE deleted_at IS NULL;
CREATE INDEX items_title_trgm_idx ON items USING gin (lower(title) gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX items_trash_batch_idx ON items (trash_batch_id) WHERE trash_batch_id IS NOT NULL;

CREATE TABLE item_content_versions (
  item_id     UUID        NOT NULL,
  version     INTEGER     NOT NULL,
  body        TEXT        NOT NULL,
  size_bytes  INTEGER     NOT NULL, -- octet_length(body)
  sha256      BYTEA       NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT item_content_versions_pkey PRIMARY KEY (item_id, version),
  CONSTRAINT item_content_versions_item_id_fkey FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE,
  CONSTRAINT item_content_versions_version_check CHECK (version >= 1),
  CONSTRAINT item_content_versions_size_bytes_check CHECK (size_bytes BETWEEN 0 AND 1048576)
);
