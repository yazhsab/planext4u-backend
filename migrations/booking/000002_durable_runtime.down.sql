SET ROLE planext4u_booking_owner;
DROP INDEX IF EXISTS booking.booking_pending_payment_expiry_idx;
ALTER TABLE booking.bookings
    DROP COLUMN IF EXISTS free_reschedules_left,
    DROP COLUMN IF EXISTS start_otp_ciphertext,
    DROP COLUMN IF EXISTS payment_expires_at,
    DROP COLUMN IF EXISTS payment_snapshot;
DROP TABLE IF EXISTS booking.policies;
ALTER TABLE booking.offerings
    DROP COLUMN IF EXISTS live_engagements,
    DROP COLUMN IF EXISTS completed_bookings,
    DROP COLUMN IF EXISTS rating_average,
    DROP COLUMN IF EXISTS verified_provider,
    DROP COLUMN IF EXISTS provider_name;
RESET ROLE;
