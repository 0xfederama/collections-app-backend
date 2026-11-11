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
