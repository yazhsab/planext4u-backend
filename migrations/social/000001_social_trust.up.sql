CREATE SCHEMA IF NOT EXISTS social AUTHORIZATION planext4u_social_owner;
ALTER SCHEMA social OWNER TO planext4u_social_owner;
REVOKE ALL ON SCHEMA social FROM PUBLIC;
SET ROLE planext4u_social_owner;

CREATE TABLE social.profiles (
    identity_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    handle text NOT NULL,
    display_name text NOT NULL,
    bio text NOT NULL DEFAULT '',
    avatar_asset_id uuid,
    private boolean NOT NULL DEFAULT false,
    verified boolean NOT NULL DEFAULT false,
    follower_count integer NOT NULL DEFAULT 0 CHECK (follower_count >= 0),
    following_count integer NOT NULL DEFAULT 0 CHECK (following_count >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, handle)
);

CREATE TABLE social.follows (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    follower_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    following_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    status text NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (follower_identity_id <> following_identity_id),
    UNIQUE (tenant_id, country, follower_identity_id, following_identity_id)
);

CREATE TABLE social.relationship_controls (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    actor_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    target_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    control text NOT NULL CHECK (control IN ('BLOCK', 'MUTE')),
    created_at timestamptz NOT NULL,
    CHECK (actor_identity_id <> target_identity_id),
    PRIMARY KEY (tenant_id, country, actor_identity_id, target_identity_id)
);

CREATE TABLE social.posts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    author_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    revision bigint NOT NULL CHECK (revision > 0),
    body text NOT NULL CHECK (char_length(body) <= 5000),
    media_asset_ids uuid[] NOT NULL DEFAULT '{}',
    hashtags text[] NOT NULL DEFAULT '{}',
    mentions text[] NOT NULL DEFAULT '{}',
    product_sticker_id uuid,
    sponsored boolean NOT NULL DEFAULT false,
    sponsor_label text,
    status text NOT NULL CHECK (status IN ('PUBLISHED', 'PENDING_REVIEW', 'REMOVED')),
    moderation_reason text,
    ranking_version text NOT NULL,
    like_count integer NOT NULL DEFAULT 0 CHECK (like_count >= 0),
    comment_count integer NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX social_feed_idx ON social.posts (tenant_id, country, status, created_at DESC);
CREATE INDEX social_author_posts_idx ON social.posts (tenant_id, country, author_identity_id, created_at DESC);

CREATE TABLE social.post_engagement (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    post_id uuid NOT NULL REFERENCES social.posts(id) ON DELETE CASCADE,
    actor_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    kind text NOT NULL CHECK (kind IN ('LIKE', 'SAVE')),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, post_id, actor_identity_id, kind)
);

CREATE TABLE social.comments (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    post_id uuid NOT NULL REFERENCES social.posts(id) ON DELETE CASCADE,
    parent_id uuid REFERENCES social.comments(id) ON DELETE CASCADE,
    author_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    depth smallint NOT NULL CHECK (depth BETWEEN 0 AND 2),
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    mentions text[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('PUBLISHED', 'REMOVED')),
    created_at timestamptz NOT NULL
);
CREATE INDEX social_post_comments_idx ON social.comments (post_id, created_at);

CREATE TABLE social.reports (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    post_id uuid NOT NULL REFERENCES social.posts(id),
    reporter_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    reason text NOT NULL,
    details text NOT NULL,
    status text NOT NULL CHECK (status IN ('OPEN', 'DECIDED')),
    decision text CHECK (decision IN ('REMOVE', 'DISMISS')),
    decision_note text,
    decided_by_identity_id uuid,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, post_id, reporter_identity_id)
);
CREATE INDEX social_moderation_queue_idx ON social.reports (tenant_id, country, status, created_at);

CREATE TABLE social.idempotency_records (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    response_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, subject_id, operation, idempotency_key)
);

GRANT USAGE ON SCHEMA social TO planext4u_social_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA social TO planext4u_social_runtime;
RESET ROLE;
