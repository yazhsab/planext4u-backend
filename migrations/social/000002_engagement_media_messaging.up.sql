SET ROLE planext4u_social_owner;

CREATE TABLE social.media_jobs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    media_asset_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('IMAGE', 'VIDEO', 'VOICE')),
    state text NOT NULL CHECK (state IN ('QUARANTINED', 'READY', 'REJECTED', 'TOMBSTONED')),
    scan_status text NOT NULL,
    blur_status text NOT NULL,
    transcode_status text NOT NULL,
    moderated_by_identity_id uuid,
    appeal_status text CHECK (appeal_status IN ('PENDING', 'APPROVED', 'REJECTED')),
    retention_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX social_media_processing_idx ON social.media_jobs (tenant_id, country, state, created_at);

CREATE TABLE social.ephemeral_content (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    author_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    media_job_id uuid NOT NULL REFERENCES social.media_jobs(id),
    kind text NOT NULL CHECK (kind IN ('STORY', 'REEL')),
    caption text NOT NULL CHECK (char_length(caption) <= 1000),
    status text NOT NULL CHECK (status IN ('PUBLISHED', 'EXPIRED', 'TOMBSTONED')),
    highlighted boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX social_ephemeral_active_idx ON social.ephemeral_content (tenant_id, country, status, expires_at);

CREATE TABLE social.collections (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 2 AND 80),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE social.collection_posts (
    collection_id uuid NOT NULL REFERENCES social.collections(id) ON DELETE CASCADE,
    post_id uuid NOT NULL REFERENCES social.posts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (collection_id, post_id)
);

CREATE TABLE social.conversations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    participant_ids uuid[] NOT NULL CHECK (cardinality(participant_ids) = 2),
    requested_by_identity_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('REQUESTED', 'ACCEPTED', 'DECLINED', 'BLOCKED')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX social_conversation_participants_gin ON social.conversations USING gin (participant_ids);

CREATE TABLE social.direct_messages (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    conversation_id uuid NOT NULL REFERENCES social.conversations(id) ON DELETE CASCADE,
    sender_identity_id uuid NOT NULL,
    body text NOT NULL CHECK (char_length(body) <= 4000),
    voice_media_job_id uuid REFERENCES social.media_jobs(id),
    status text NOT NULL CHECK (status IN ('DELIVERED', 'READ', 'REMOVED')),
    created_at timestamptz NOT NULL
);
CREATE INDEX social_messages_timeline_idx ON social.direct_messages (conversation_id, created_at);

CREATE TABLE social.call_sessions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    conversation_id uuid NOT NULL REFERENCES social.conversations(id) ON DELETE CASCADE,
    initiator_identity_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('AUDIO', 'VIDEO')),
    status text NOT NULL CHECK (status IN ('RINGING', 'CONNECTED', 'ENDED')),
    signal_count integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA social TO planext4u_social_runtime;
RESET ROLE;
