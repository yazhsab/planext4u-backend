SET ROLE planext4u_fulfillment_owner;
DROP TABLE IF EXISTS fulfillment.field_check_ins;
DROP TABLE IF EXISTS fulfillment.payout_entry_claims;
DROP INDEX IF EXISTS fulfillment.fulfillment_one_active_duty_per_rider;
DROP TABLE IF EXISTS fulfillment.policies;
RESET ROLE;
