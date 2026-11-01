-- Baseline of the F-019 refactor, produced from the final schema of the former
-- migrations 000001..000025. Later schema changes append new migrations after this file.
-- The former 000001..000025 chain is in git history before the baseline commit.
-- The baseline carries no logic: no triggers, no functions, no ON DELETE CASCADE.
-- Built-in types are seeded from Go (internal/seed) and deletes are explicit (D-051).

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';

CREATE TABLE public.attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    storage_key text NOT NULL,
    thumbnail_storage_key text NOT NULL,
    original_filename text,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    sha256 bytea NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    CONSTRAINT attachments_height_check CHECK ((height > 0)),
    CONSTRAINT attachments_media_type_check CHECK ((media_type = ANY (ARRAY['image/jpeg'::text, 'image/png'::text, 'image/webp'::text]))),
    CONSTRAINT attachments_original_filename_check CHECK ((char_length(original_filename) <= 255)),
    CONSTRAINT attachments_size_bytes_check CHECK (((size_bytes >= 1) AND (size_bytes <= 10485760))),
    CONSTRAINT attachments_width_check CHECK ((width > 0))
);

CREATE TABLE public.folders (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    parent_folder_id uuid,
    name text NOT NULL,
    sort_order integer NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    deleted_at TIMESTAMPTZ,
    trash_batch_id uuid,
    CONSTRAINT folders_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200))),
    CONSTRAINT folders_no_self_parent_check CHECK ((parent_folder_id IS DISTINCT FROM id)),
    CONSTRAINT folders_trash_check CHECK (((deleted_at IS NULL) = (trash_batch_id IS NULL)))
);

CREATE TABLE public.item_content_versions (
    item_id uuid NOT NULL,
    version integer NOT NULL,
    body text NOT NULL,
    size_bytes integer NOT NULL,
    sha256 bytea NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    CONSTRAINT item_content_versions_size_bytes_check CHECK (((size_bytes >= 0) AND (size_bytes <= 1048576))),
    CONSTRAINT item_content_versions_version_check CHECK ((version >= 1))
);

CREATE TABLE public.items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    list_id uuid NOT NULL,
    title text NOT NULL,
    image_url text,
    image_attachment_id uuid,
    price_min numeric(12,2),
    price_max numeric(12,2),
    currency character(3),
    website_url text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    sort_order integer NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    deleted_at TIMESTAMPTZ,
    trash_batch_id uuid,
    CONSTRAINT items_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT items_image_source_check CHECK ((NOT ((image_url IS NOT NULL) AND (image_attachment_id IS NOT NULL)))),
    CONSTRAINT items_image_url_check CHECK ((char_length(image_url) <= 2048)),
    CONSTRAINT items_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT items_price_currency_check CHECK ((((price_min IS NULL) AND (price_max IS NULL)) OR (currency IS NOT NULL))),
    CONSTRAINT items_price_max_check CHECK ((price_max >= (0)::numeric)),
    CONSTRAINT items_price_min_check CHECK ((price_min >= (0)::numeric)),
    CONSTRAINT items_price_range_check CHECK (((price_min IS NULL) OR (price_max IS NULL) OR (price_min <= price_max))),
    CONSTRAINT items_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 300))),
    CONSTRAINT items_trash_check CHECK (((deleted_at IS NULL) = (trash_batch_id IS NULL))),
    CONSTRAINT items_website_url_check CHECK ((char_length(website_url) <= 2048))
);

CREATE TABLE public.lists (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    folder_id uuid,
    name text NOT NULL,
    description text,
    display_mode text DEFAULT 'thumbnail'::text NOT NULL,
    sort_mode text DEFAULT 'manual'::text NOT NULL,
    sort_order integer NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    deleted_at TIMESTAMPTZ,
    trash_batch_id uuid,
    type text,
    icon text,
    icon_color text,
    CONSTRAINT lists_description_check CHECK ((char_length(description) <= 65536)),
    CONSTRAINT lists_display_mode_check CHECK ((display_mode = ANY (ARRAY['compact'::text, 'thumbnail'::text, 'grid'::text]))),
    CONSTRAINT lists_icon_color_check CHECK (((icon_color IS NULL) OR ((icon IS NOT NULL) AND (icon ~~ 'i:%'::text) AND (icon_color = ANY (ARRAY['red'::text, 'orange'::text, 'amber'::text, 'green'::text, 'teal'::text, 'blue'::text, 'indigo'::text, 'purple'::text, 'pink'::text, 'gray'::text]))))),
    CONSTRAINT lists_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200))),
    CONSTRAINT lists_trash_check CHECK (((deleted_at IS NULL) = (trash_batch_id IS NULL))),
    CONSTRAINT lists_type_check CHECK ((type ~ '^[a-z][a-z0-9_]{0,31}$'::text))
);

CREATE TABLE public.lookup_imports (
    workspace_id uuid CONSTRAINT tmdb_imports_workspace_id_not_null NOT NULL,
    item_id uuid CONSTRAINT tmdb_imports_item_id_not_null NOT NULL,
    type text CONSTRAINT tmdb_imports_kind_not_null NOT NULL,
    imported_fields jsonb DEFAULT '{}'::jsonb CONSTRAINT tmdb_imports_imported_fields_not_null NOT NULL,
    imported_title text,
    original_title text CONSTRAINT tmdb_imports_original_title_not_null NOT NULL,
    attachment_id uuid,
    poster_path text,
    poster_fetched_at TIMESTAMPTZ,
    poster_refresh_after TIMESTAMPTZ,
    poster_expires_at TIMESTAMPTZ,
    fetched_at TIMESTAMPTZ CONSTRAINT tmdb_imports_fetched_at_not_null NOT NULL,
    refresh_after TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    provider text DEFAULT 'tmdb'::text NOT NULL,
    source_id text NOT NULL,
    CONSTRAINT lookup_imports_imported_fields_check CHECK ((jsonb_typeof(imported_fields) = 'object'::text)),
    CONSTRAINT lookup_imports_imported_title_check CHECK (((imported_title IS NULL) OR ((char_length(imported_title) >= 1) AND (char_length(imported_title) <= 300)))),
    CONSTRAINT lookup_imports_original_title_check CHECK (((char_length(original_title) >= 1) AND (char_length(original_title) <= 300))),
    CONSTRAINT lookup_imports_poster_check CHECK ((((attachment_id IS NULL) AND (poster_path IS NULL) AND (poster_fetched_at IS NULL) AND (poster_refresh_after IS NULL) AND (poster_expires_at IS NULL)) OR ((attachment_id IS NOT NULL) AND (poster_path IS NOT NULL) AND (poster_fetched_at IS NOT NULL) AND (((provider = 'tmdb'::text) AND (poster_refresh_after IS NOT NULL) AND (poster_expires_at IS NOT NULL)) OR ((provider <> 'tmdb'::text) AND (poster_refresh_after IS NULL) AND (poster_expires_at IS NULL)))))),
    CONSTRAINT lookup_imports_poster_retention_check CHECK (((poster_fetched_at IS NULL) OR ((poster_fetched_at < poster_refresh_after) AND (poster_refresh_after < poster_expires_at)))),
    CONSTRAINT lookup_imports_provider_check CHECK ((provider = ANY (ARRAY['tmdb'::text, 'openlibrary'::text]))),
    CONSTRAINT lookup_imports_provider_type_check CHECK ((((provider = 'tmdb'::text) AND (type = ANY (ARRAY['movie'::text, 'tv_show'::text]))) OR ((provider = 'openlibrary'::text) AND (type = 'book'::text)))),
    CONSTRAINT lookup_imports_retention_check CHECK ((((provider = 'tmdb'::text) AND (refresh_after IS NOT NULL) AND (expires_at IS NOT NULL) AND (fetched_at < refresh_after) AND (refresh_after < expires_at)) OR ((provider <> 'tmdb'::text) AND (refresh_after IS NULL) AND (expires_at IS NULL)))),
    CONSTRAINT lookup_imports_source_id_check CHECK (((char_length(source_id) >= 1) AND (char_length(source_id) <= 64) AND ((provider <> 'tmdb'::text) OR (source_id ~ '^[1-9][0-9]{0,9}$'::text)) AND ((provider <> 'openlibrary'::text) OR (source_id ~ '^OL[0-9]+W$'::text)))),
    CONSTRAINT lookup_imports_type_check CHECK ((type = ANY (ARRAY['movie'::text, 'tv_show'::text, 'book'::text])))
);

CREATE TABLE public.trash_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    root_type text NOT NULL,
    root_id uuid NOT NULL,
    root_name text NOT NULL,
    former_path jsonb NOT NULL,
    deleted_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    CONSTRAINT trash_batches_root_type_check CHECK ((root_type = ANY (ARRAY['folder'::text, 'list'::text, 'item'::text])))
);

CREATE TABLE public.type_definitions (
    id uuid DEFAULT gen_random_uuid() CONSTRAINT kind_definitions_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT kind_definitions_workspace_id_not_null NOT NULL,
    key text CONSTRAINT kind_definitions_key_not_null NOT NULL,
    name text CONSTRAINT kind_definitions_name_not_null NOT NULL,
    default_icon text,
    allows_price boolean DEFAULT false CONSTRAINT kind_definitions_allows_price_not_null NOT NULL,
    default_sort_field text,
    is_locked boolean DEFAULT false CONSTRAINT kind_definitions_is_locked_not_null NOT NULL,
    version integer DEFAULT 1 CONSTRAINT kind_definitions_version_not_null NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() CONSTRAINT kind_definitions_created_at_not_null NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() CONSTRAINT kind_definitions_updated_at_not_null NOT NULL,
    CONSTRAINT type_definitions_default_sort_field_check CHECK (((default_sort_field IS NULL) OR (default_sort_field ~ '^[a-z][a-z0-9_]{0,31}$'::text))),
    CONSTRAINT type_definitions_key_check CHECK ((key ~ '^[a-z][a-z0-9_]{0,31}$'::text)),
    CONSTRAINT type_definitions_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT type_definitions_version_check CHECK ((version >= 1))
);

CREATE TABLE public.type_fields (
    id uuid DEFAULT gen_random_uuid() CONSTRAINT kind_fields_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT kind_fields_workspace_id_not_null NOT NULL,
    type_id uuid CONSTRAINT kind_fields_kind_id_not_null NOT NULL,
    key text CONSTRAINT kind_fields_key_not_null NOT NULL,
    label text CONSTRAINT kind_fields_label_not_null NOT NULL,
    field_type text CONSTRAINT kind_fields_field_type_not_null NOT NULL,
    "position" integer CONSTRAINT kind_fields_position_not_null NOT NULL,
    is_locked boolean DEFAULT false CONSTRAINT kind_fields_is_locked_not_null NOT NULL,
    version integer DEFAULT 1 CONSTRAINT kind_fields_version_not_null NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() CONSTRAINT kind_fields_created_at_not_null NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() CONSTRAINT kind_fields_updated_at_not_null NOT NULL,
    CONSTRAINT type_fields_custom_field_type_check CHECK ((is_locked OR (field_type = ANY (ARRAY['text'::text, 'number'::text, 'date'::text, 'boolean'::text, 'link'::text])))),
    CONSTRAINT type_fields_field_type_check CHECK ((field_type = ANY (ARRAY['text'::text, 'number'::text, 'date'::text, 'boolean'::text, 'link'::text, 'streaming'::text, 'stores'::text]))),
    CONSTRAINT type_fields_key_check CHECK ((key ~ '^[a-z][a-z0-9_]{0,31}$'::text)),
    CONSTRAINT type_fields_label_check CHECK (((char_length(label) >= 1) AND (char_length(label) <= 100))),
    CONSTRAINT type_fields_position_check CHECK (("position" >= 0)),
    CONSTRAINT type_fields_version_check CHECK ((version >= 1))
);

CREATE TABLE public.workspace_settings (
    workspace_id uuid NOT NULL,
    streaming_country_code character(2) DEFAULT 'IT'::bpchar NOT NULL,
    markdown_history_limit integer DEFAULT 20 NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    trash_retention_days integer,
    metadata_language text DEFAULT 'it-IT'::text NOT NULL,
    preferred_store text DEFAULT 'amazon'::text NOT NULL,
    CONSTRAINT workspace_settings_markdown_history_limit_check CHECK (((markdown_history_limit >= 0) AND (markdown_history_limit <= 200))),
    CONSTRAINT workspace_settings_metadata_language_check CHECK ((metadata_language ~ '^[a-z]{2}(-[A-Z]{2})?$'::text)),
    CONSTRAINT workspace_settings_preferred_store_check CHECK ((preferred_store = ANY (ARRAY['amazon'::text, 'bookshop'::text, 'apple_books'::text, 'google_play_books'::text, 'kobo'::text]))),
    CONSTRAINT workspace_settings_streaming_country_code_check CHECK ((streaming_country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT workspace_settings_trash_retention_days_check CHECK (((trash_retention_days IS NULL) OR ((trash_retention_days >= 1) AND (trash_retention_days <= 365))))
);

CREATE TABLE public.workspaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    CONSTRAINT workspaces_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200)))
);

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_storage_key_key UNIQUE (storage_key);

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_thumbnail_storage_key_key UNIQUE (thumbnail_storage_key);

ALTER TABLE ONLY public.folders
    ADD CONSTRAINT folders_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.folders
    ADD CONSTRAINT folders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.item_content_versions
    ADD CONSTRAINT item_content_versions_pkey PRIMARY KEY (item_id, version);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_image_attachment_id_key UNIQUE (image_attachment_id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lookup_imports
    ADD CONSTRAINT lookup_imports_pkey PRIMARY KEY (item_id);

ALTER TABLE ONLY public.trash_batches
    ADD CONSTRAINT trash_batches_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.trash_batches
    ADD CONSTRAINT trash_batches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.type_definitions
    ADD CONSTRAINT type_definitions_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.type_definitions
    ADD CONSTRAINT type_definitions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.type_definitions
    ADD CONSTRAINT type_definitions_workspace_id_key_key UNIQUE (workspace_id, key);

ALTER TABLE ONLY public.type_fields
    ADD CONSTRAINT type_fields_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.type_fields
    ADD CONSTRAINT type_fields_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.type_fields
    ADD CONSTRAINT type_fields_type_id_key_key UNIQUE (type_id, key);

ALTER TABLE ONLY public.type_fields
    ADD CONSTRAINT type_fields_type_id_position_key UNIQUE (type_id, "position");

ALTER TABLE ONLY public.workspace_settings
    ADD CONSTRAINT workspace_settings_pkey PRIMARY KEY (workspace_id);

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);

CREATE INDEX folders_active_parent_idx ON public.folders USING btree (workspace_id, parent_folder_id, sort_order) WHERE (deleted_at IS NULL);

CREATE INDEX folders_trash_batch_idx ON public.folders USING btree (trash_batch_id) WHERE (trash_batch_id IS NOT NULL);

CREATE INDEX items_active_list_idx ON public.items USING btree (list_id, sort_order) WHERE (deleted_at IS NULL);

CREATE INDEX items_title_trgm_idx ON public.items USING gin (lower(title) public.gin_trgm_ops) WHERE (deleted_at IS NULL);

CREATE INDEX items_trash_batch_idx ON public.items USING btree (trash_batch_id) WHERE (trash_batch_id IS NOT NULL);

CREATE INDEX lists_active_folder_idx ON public.lists USING btree (workspace_id, folder_id, sort_order) WHERE (deleted_at IS NULL);

CREATE INDEX lists_trash_batch_idx ON public.lists USING btree (trash_batch_id) WHERE (trash_batch_id IS NOT NULL);

CREATE INDEX lookup_imports_due_idx ON public.lookup_imports USING btree (workspace_id, refresh_after, item_id);

CREATE INDEX lookup_imports_expiry_idx ON public.lookup_imports USING btree (workspace_id, expires_at, item_id);

CREATE INDEX lookup_imports_poster_due_idx ON public.lookup_imports USING btree (workspace_id, poster_refresh_after, item_id) WHERE (poster_refresh_after IS NOT NULL);

CREATE INDEX lookup_imports_poster_expiry_idx ON public.lookup_imports USING btree (workspace_id, poster_expires_at, item_id) WHERE (poster_expires_at IS NOT NULL);

CREATE INDEX trash_batches_workspace_deleted_idx ON public.trash_batches USING btree (workspace_id, deleted_at DESC);

CREATE INDEX type_definitions_workspace_idx ON public.type_definitions USING btree (workspace_id, key);

CREATE INDEX type_fields_workspace_type_position_idx ON public.type_fields USING btree (workspace_id, type_id, "position");

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.folders
    ADD CONSTRAINT folders_parent_fkey FOREIGN KEY (parent_folder_id, workspace_id) REFERENCES public.folders(id, workspace_id);

ALTER TABLE ONLY public.folders
    ADD CONSTRAINT folders_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES public.trash_batches(id);

ALTER TABLE ONLY public.folders
    ADD CONSTRAINT folders_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.item_content_versions
    ADD CONSTRAINT item_content_versions_item_id_fkey FOREIGN KEY (item_id) REFERENCES public.items(id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_image_attachment_fkey FOREIGN KEY (image_attachment_id, workspace_id) REFERENCES public.attachments(id, workspace_id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_list_fkey FOREIGN KEY (list_id, workspace_id) REFERENCES public.lists(id, workspace_id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES public.trash_batches(id);

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_folder_fkey FOREIGN KEY (folder_id, workspace_id) REFERENCES public.folders(id, workspace_id);

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_trash_batch_id_fkey FOREIGN KEY (trash_batch_id) REFERENCES public.trash_batches(id);

ALTER TABLE ONLY public.lists
    ADD CONSTRAINT lists_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.lookup_imports
    ADD CONSTRAINT lookup_imports_item_workspace_fkey FOREIGN KEY (item_id, workspace_id) REFERENCES public.items(id, workspace_id);

ALTER TABLE ONLY public.trash_batches
    ADD CONSTRAINT trash_batches_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.type_definitions
    ADD CONSTRAINT type_definitions_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

ALTER TABLE ONLY public.type_fields
    ADD CONSTRAINT type_fields_type_workspace_fkey FOREIGN KEY (type_id, workspace_id) REFERENCES public.type_definitions(id, workspace_id);

ALTER TABLE ONLY public.workspace_settings
    ADD CONSTRAINT workspace_settings_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);

