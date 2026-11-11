-- F-027 accounts (structure only, D-115/D-147): one row per person who signed in through
-- Cloudflare Access, keyed by lower-cased e-mail inside the single workspace. Later F-027
-- stories (ownership, shares, authorship) edit this file in place until the release.
CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    email text NOT NULL,
    name text,
    avatar_url text,
    profile_fetched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_workspace_fk FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT users_email_check CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 320),
    CONSTRAINT users_name_check CHECK (char_length(name) <= 200),
    CONSTRAINT users_avatar_url_check CHECK (char_length(avatar_url) <= 2048),
    CONSTRAINT users_workspace_email_key UNIQUE (workspace_id, email),
    CONSTRAINT users_id_workspace_key UNIQUE (id, workspace_id)
);

-- Ownership (F-027 B2, D-147): only top-level folders and lists carry an owner; anything nested
-- inherits access from its top-level ancestor (the "box"). Nullable, so the previous release's
-- inserts (which know no owner) keep working; the startup backfill (Go, decision 15) fills the
-- existing top-level rows, this migration touches no data. No CHECK ties the owner to the parent
-- for the same reason.
ALTER TABLE folders ADD COLUMN owner_user_id uuid;
ALTER TABLE folders ADD CONSTRAINT folders_owner_fk
    FOREIGN KEY (owner_user_id, workspace_id) REFERENCES users(id, workspace_id);
CREATE INDEX folders_owner_idx ON folders (workspace_id, owner_user_id) WHERE owner_user_id IS NOT NULL;

ALTER TABLE lists ADD COLUMN owner_user_id uuid;
ALTER TABLE lists ADD CONSTRAINT lists_owner_fk
    FOREIGN KEY (owner_user_id, workspace_id) REFERENCES users(id, workspace_id);
CREATE INDEX lists_owner_idx ON lists (workspace_id, owner_user_id) WHERE owner_user_id IS NOT NULL;

-- Per-user order of the top-level entries (decision 7). entry_id has no foreign key to folders or
-- lists on purpose: the previous release's purge deletes those rows without knowing this table,
-- so the new purge deletes the order rows explicitly (D-118).
CREATE TABLE user_top_order (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    entry_type text NOT NULL,
    entry_id uuid NOT NULL,
    sort_order integer NOT NULL,
    CONSTRAINT user_top_order_pkey PRIMARY KEY (user_id, entry_type, entry_id),
    CONSTRAINT user_top_order_type_check CHECK (entry_type IN ('folder', 'list')),
    CONSTRAINT user_top_order_user_fk FOREIGN KEY (user_id, workspace_id) REFERENCES users(id, workspace_id)
);
CREATE INDEX user_top_order_entry_idx ON user_top_order (workspace_id, entry_id);
