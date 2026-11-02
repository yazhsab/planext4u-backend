SET ROLE planext4u_fulfillment_owner;
DROP TABLE fulfillment.rider_submissions;
ALTER TABLE fulfillment.rider_profiles DROP CONSTRAINT rider_rework_only_suspended,
 DROP COLUMN rework_allowed,DROP COLUMN reviewed_at,DROP COLUMN review_reason;
RESET ROLE;
