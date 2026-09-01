SET ROLE planext4u_notification_owner;

ALTER TABLE notification.deliveries ADD COLUMN attempts integer;
ALTER TABLE notification.deliveries ADD COLUMN next_attempt_at timestamptz;
ALTER TABLE notification.deliveries ADD COLUMN claim_until timestamptz;

UPDATE notification.deliveries
SET attempts = 0,
    next_attempt_at = created_at;

ALTER TABLE notification.deliveries ALTER COLUMN attempts SET DEFAULT 0;
ALTER TABLE notification.deliveries ALTER COLUMN next_attempt_at SET DEFAULT now();
ALTER TABLE notification.deliveries ADD CONSTRAINT notification_delivery_retry_fields_present
    CHECK (attempts IS NOT NULL AND next_attempt_at IS NOT NULL) NOT VALID;
ALTER TABLE notification.deliveries VALIDATE CONSTRAINT notification_delivery_retry_fields_present;
ALTER TABLE notification.deliveries ADD CONSTRAINT notification_delivery_attempts_bounded
    CHECK (attempts BETWEEN 0 AND 20);

CREATE INDEX notification_delivery_worker_idx
    ON notification.deliveries (next_attempt_at, created_at, id)
    WHERE status IN ('QUEUED', 'SENDING');

RESET ROLE;
