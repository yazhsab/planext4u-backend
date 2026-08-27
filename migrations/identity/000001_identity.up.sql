CREATE SCHEMA IF NOT EXISTS identity AUTHORIZATION planext4u_identity_owner;
ALTER SCHEMA identity OWNER TO planext4u_identity_owner;
REVOKE ALL ON SCHEMA identity FROM PUBLIC;
SET ROLE planext4u_identity_owner;

CREATE TABLE identity.identities (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    provider text NOT NULL,
    provider_subject text NOT NULL,
    created_at timestamptz NOT NULL,
    disabled_at timestamptz,
    CONSTRAINT identities_provider_identity_unique UNIQUE (tenant_id, provider, provider_subject),
    CONSTRAINT identities_id_length CHECK (length(id) BETWEEN 1 AND 128),
    CONSTRAINT identities_tenant_length CHECK (length(tenant_id) BETWEEN 1 AND 128),
    CONSTRAINT identities_provider_length CHECK (length(provider) BETWEEN 1 AND 32),
    CONSTRAINT identities_provider_subject_length CHECK (length(provider_subject) BETWEEN 1 AND 128)
);

CREATE TABLE identity.profiles (
    identity_id text PRIMARY KEY REFERENCES identity.identities(id),
    display_name text NOT NULL DEFAULT '',
    locale text NOT NULL DEFAULT 'en',
    time_zone text NOT NULL DEFAULT 'Asia/Kolkata',
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    CONSTRAINT profiles_display_name_length CHECK (char_length(display_name) <= 100),
    CONSTRAINT profiles_locale_allowed CHECK (locale IN ('en', 'ta')),
    CONSTRAINT profiles_time_zone_length CHECK (length(time_zone) BETWEEN 1 AND 64),
    CONSTRAINT profiles_version_positive CHECK (version >= 1)
);

CREATE TABLE identity.identity_roles (
    identity_id text NOT NULL REFERENCES identity.identities(id),
    role text NOT NULL,
    granted_at timestamptz NOT NULL,
    PRIMARY KEY (identity_id, role),
    CONSTRAINT identity_roles_allowed CHECK (role IN ('CUSTOMER', 'VENDOR', 'RIDER', 'ADMIN'))
);

CREATE TABLE identity.sessions (
    id text PRIMARY KEY,
    identity_id text NOT NULL REFERENCES identity.identities(id),
    tenant_id text NOT NULL,
    country char(2) NOT NULL,
    device_id text NOT NULL,
    device_reference text NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    authenticated_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    revoked_at timestamptz,
    rotation bigint NOT NULL DEFAULT 0,
    CONSTRAINT sessions_identity_tenant_unique UNIQUE (id, identity_id, tenant_id),
    CONSTRAINT sessions_id_length CHECK (length(id) BETWEEN 1 AND 128),
    CONSTRAINT sessions_device_id_length CHECK (length(device_id) BETWEEN 1 AND 128),
    CONSTRAINT sessions_device_reference_length CHECK (length(device_reference) BETWEEN 1 AND 128),
    CONSTRAINT sessions_country_upper CHECK (country ~ '^[A-Z]{2}$'),
    CONSTRAINT sessions_rotation_nonnegative CHECK (rotation >= 0),
    CONSTRAINT sessions_expiry_after_auth CHECK (refresh_expires_at > authenticated_at)
);

CREATE INDEX sessions_identity_authenticated_idx
    ON identity.sessions (identity_id, authenticated_at DESC);
CREATE INDEX sessions_active_expiry_idx
    ON identity.sessions (refresh_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE identity.refresh_tokens (
    digest char(64) PRIMARY KEY,
    session_id text NOT NULL REFERENCES identity.sessions(id),
    status text NOT NULL,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CONSTRAINT refresh_tokens_status_allowed CHECK (status IN ('ACTIVE', 'CONSUMED', 'REVOKED')),
    CONSTRAINT refresh_tokens_consumed_consistent CHECK (
        (status = 'ACTIVE' AND consumed_at IS NULL) OR
        (status IN ('CONSUMED', 'REVOKED'))
    )
);

CREATE UNIQUE INDEX refresh_tokens_one_active_per_session
    ON identity.refresh_tokens (session_id)
    WHERE status = 'ACTIVE';
CREATE INDEX refresh_tokens_session_idx
    ON identity.refresh_tokens (session_id, created_at DESC);

CREATE TABLE identity.consent_evidence (
    evidence_id text PRIMARY KEY,
    identity_id text NOT NULL REFERENCES identity.identities(id),
    purpose text NOT NULL,
    granted boolean NOT NULL,
    policy_version text NOT NULL,
    recorded_at timestamptz NOT NULL,
    version bigint NOT NULL,
    CONSTRAINT consent_evidence_identity_purpose_version_unique UNIQUE (identity_id, purpose, version),
    CONSTRAINT consent_evidence_purpose_allowed CHECK (
        purpose IN ('ANALYTICS', 'MARKETING', 'LOCATION_SERVICEABILITY', 'LOCATION_DELIVERY')
    ),
    CONSTRAINT consent_evidence_policy_length CHECK (length(policy_version) BETWEEN 1 AND 64),
    CONSTRAINT consent_evidence_version_positive CHECK (version >= 1)
);

CREATE INDEX consent_evidence_current_idx
    ON identity.consent_evidence (identity_id, purpose, version DESC);

CREATE TABLE identity.security_events (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type text NOT NULL,
    identity_id text,
    session_id text,
    device_reference text,
    purpose text,
    outcome text NOT NULL,
    occurred_at timestamptz NOT NULL,
    CONSTRAINT security_events_type_length CHECK (length(event_type) BETWEEN 1 AND 64),
    CONSTRAINT security_events_outcome_length CHECK (length(outcome) BETWEEN 1 AND 32)
);

CREATE INDEX security_events_identity_time_idx
    ON identity.security_events (identity_id, occurred_at DESC);
CREATE INDEX security_events_session_time_idx
    ON identity.security_events (session_id, occurred_at DESC);

COMMENT ON COLUMN identity.refresh_tokens.digest IS 'HMAC-SHA256 only; plaintext refresh tokens are prohibited.';
COMMENT ON COLUMN identity.sessions.device_id IS 'Confidential platform device identifier; never emit to general logs or API responses.';
COMMENT ON TABLE identity.security_events IS 'Identity-local append-only evidence; privileged audit export is owned by the audit service.';

GRANT USAGE ON SCHEMA identity TO planext4u_identity_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA identity TO planext4u_identity_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA identity TO planext4u_identity_runtime;
RESET ROLE;
