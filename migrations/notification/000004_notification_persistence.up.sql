ALTER TABLE notification.push_device OWNER TO planext4u_notification_owner;
ALTER TABLE notification.deliveries OWNER TO planext4u_notification_owner;

SET ROLE planext4u_notification_owner;

CREATE TABLE notification.consents (
    tenant_id uuid NOT NULL,
    subject_id text NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('MARKETING')),
    granted boolean NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, subject_id, purpose)
);

CREATE INDEX notification_push_device_subject_idx
    ON notification.push_device (tenant_id, country, subject_id, updated_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON notification.push_device TO planext4u_notification_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON notification.deliveries TO planext4u_notification_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON notification.consents TO planext4u_notification_runtime;

RESET ROLE;
