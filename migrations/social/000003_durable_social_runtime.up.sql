SET ROLE planext4u_social_owner;

CREATE TABLE social.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    version text NOT NULL,
    ranking_model text NOT NULL,
    review_terms text[] NOT NULL DEFAULT '{}',
    story_ttl_seconds integer NOT NULL CHECK (story_ttl_seconds BETWEEN 300 AND 604800),
    reel_ttl_seconds integer NOT NULL CHECK (reel_ttl_seconds BETWEEN 3600 AND 7776000),
    media_retention_seconds integer NOT NULL CHECK (media_retention_seconds BETWEEN 86400 AND 31536000),
    presence_ttl_seconds integer NOT NULL CHECK (presence_ttl_seconds BETWEEN 15 AND 900),
    call_ttl_seconds integer NOT NULL CHECK (call_ttl_seconds BETWEEN 30 AND 3600),
    like_reward_points integer NOT NULL DEFAULT 0 CHECK (like_reward_points BETWEEN 0 AND 10000),
    follow_reward_points integer NOT NULL DEFAULT 0 CHECK (follow_reward_points BETWEEN 0 AND 10000),
    share_reward_points integer NOT NULL DEFAULT 0 CHECK (share_reward_points BETWEEN 0 AND 10000),
    reward_expiry_seconds integer NOT NULL DEFAULT 31536000 CHECK (reward_expiry_seconds BETWEEN 86400 AND 94608000),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country)
);

CREATE TABLE social.presence (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    profile_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id) ON DELETE CASCADE,
    state text NOT NULL CHECK (state IN ('ONLINE', 'AWAY', 'OFFLINE')),
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, profile_identity_id)
);
CREATE INDEX social_presence_expiry_idx ON social.presence (tenant_id, country, expires_at);

CREATE TABLE social.call_signals (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    call_id uuid NOT NULL REFERENCES social.call_sessions(id) ON DELETE CASCADE,
    sender_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    signal_type text NOT NULL CHECK (signal_type IN ('OFFER', 'ANSWER', 'ICE', 'END')),
    payload_digest char(64) NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX social_call_signals_timeline_idx ON social.call_signals (call_id, sequence_id);

CREATE TABLE social.post_shares (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    post_id uuid NOT NULL REFERENCES social.posts(id) ON DELETE CASCADE,
    actor_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    channel text NOT NULL CHECK (channel IN ('IN_APP', 'LINK', 'EXTERNAL')),
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, post_id, actor_identity_id, channel)
);

CREATE TABLE social.reward_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    actor_identity_id uuid NOT NULL REFERENCES social.profiles(identity_id),
    event_reference text NOT NULL,
    device_reference text NOT NULL,
    points bigint NOT NULL CHECK (points > 0),
    expires_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING', 'APPLIED', 'REJECTED')),
    wallet_entry_id uuid,
    last_error text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, event_reference)
);
CREATE INDEX social_reward_delivery_idx ON social.reward_events (status, next_attempt_at, created_at);

CREATE UNIQUE INDEX social_conversation_pair_unique
    ON social.conversations (
        tenant_id,
        country,
        LEAST(participant_ids[1], participant_ids[2]),
        GREATEST(participant_ids[1], participant_ids[2])
    );

ALTER TABLE social.media_jobs ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

GRANT SELECT, INSERT, UPDATE, DELETE ON social.policies, social.presence, social.call_signals, social.post_shares, social.reward_events TO planext4u_social_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA social TO planext4u_social_runtime;

RESET ROLE;
