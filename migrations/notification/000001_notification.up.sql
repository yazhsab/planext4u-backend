CREATE SCHEMA IF NOT EXISTS notification AUTHORIZATION planext4u_notification_owner;
ALTER SCHEMA notification OWNER TO planext4u_notification_owner;
REVOKE ALL ON SCHEMA notification FROM PUBLIC;
SET ROLE planext4u_notification_owner;

CREATE TABLE notification.preferences (
    tenant_id uuid NOT NULL,
    subject_id text NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('SECURITY', 'TRANSACTIONAL', 'MARKETING')),
    channel text NOT NULL CHECK (channel IN ('EMAIL', 'PUSH', 'WHATSAPP', 'IN_APP')),
    enabled boolean NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, subject_id, purpose, channel)
);

CREATE TABLE notification.templates (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    key text NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    locale text NOT NULL,
    channel text NOT NULL,
    subject text NOT NULL,
    body text NOT NULL,
    variables jsonb NOT NULL,
    published_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country, key, version, locale, channel)
);

CREATE TABLE notification.deliveries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    subject_id text NOT NULL,
    recipient_ref text NOT NULL,
    channel text NOT NULL,
    purpose text NOT NULL,
    template_key text NOT NULL,
    template_version bigint NOT NULL,
    locale text NOT NULL,
    rendered_subject text NOT NULL,
    rendered_body text NOT NULL,
    status text NOT NULL CHECK (status IN ('QUEUED', 'SENDING', 'SENT', 'DELIVERED', 'FAILED', 'SUPPRESSED')),
    suppression_reason text,
    provider_message_id text,
    last_error_code text,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX notification_deliveries_subject_idx ON notification.deliveries (tenant_id, subject_id, created_at DESC);

CREATE TABLE notification.provider_receipts (
    tenant_id uuid NOT NULL,
    receipt_id text NOT NULL,
    delivery_id uuid NOT NULL REFERENCES notification.deliveries(id),
    provider_message_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('DELIVERED', 'FAILED')),
    error_code text,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, receipt_id)
);

GRANT USAGE ON SCHEMA notification TO planext4u_notification_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA notification TO planext4u_notification_runtime;
RESET ROLE;
