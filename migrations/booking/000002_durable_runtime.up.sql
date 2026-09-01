SET ROLE planext4u_booking_owner;

ALTER TABLE booking.offerings
    ADD COLUMN provider_name text NOT NULL CHECK (char_length(provider_name) BETWEEN 2 AND 160),
    ADD COLUMN verified_provider boolean NOT NULL DEFAULT false,
    ADD COLUMN rating_average numeric(3,2) NOT NULL DEFAULT 0 CHECK (rating_average BETWEEN 0 AND 5),
    ADD COLUMN completed_bookings integer NOT NULL DEFAULT 0 CHECK (completed_bookings >= 0),
    ADD COLUMN live_engagements integer NOT NULL DEFAULT 0 CHECK (live_engagements >= 0);

CREATE TABLE booking.policies (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    version text NOT NULL CHECK (char_length(version) BETWEEN 1 AND 128),
    hold_ttl_seconds integer NOT NULL CHECK (hold_ttl_seconds BETWEEN 60 AND 1800),
    cancellation_cutoff_seconds integer NOT NULL CHECK (cancellation_cutoff_seconds BETWEEN 0 AND 2592000),
    maximum_free_reschedules integer NOT NULL CHECK (maximum_free_reschedules BETWEEN 0 AND 10),
    start_otp_validity_seconds integer NOT NULL CHECK (start_otp_validity_seconds BETWEEN 60 AND 86400),
    completion_confirm_seconds integer NOT NULL CHECK (completion_confirm_seconds BETWEEN 3600 AND 2592000),
    wallet_point_value_minor bigint NOT NULL CHECK (wallet_point_value_minor > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, country)
);

ALTER TABLE booking.bookings
    ADD COLUMN payment_snapshot jsonb NOT NULL CHECK (jsonb_typeof(payment_snapshot) = 'object'),
    ADD COLUMN payment_expires_at timestamptz NOT NULL,
    ADD COLUMN start_otp_ciphertext bytea,
    ADD COLUMN free_reschedules_left integer NOT NULL DEFAULT 0 CHECK (free_reschedules_left >= 0);

CREATE INDEX booking_pending_payment_expiry_idx
    ON booking.bookings (payment_expires_at)
    WHERE status = 'PENDING_PAYMENT';

GRANT SELECT, INSERT, UPDATE, DELETE ON booking.policies TO planext4u_booking_runtime;
RESET ROLE;
