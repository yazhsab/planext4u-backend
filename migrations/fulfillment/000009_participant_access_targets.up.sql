SET ROLE planext4u_fulfillment_owner;
CREATE VIEW fulfillment.participant_access_targets AS SELECT tenant_id,country,rider_identity_id AS id FROM fulfillment.rider_profiles;
GRANT USAGE ON SCHEMA fulfillment TO planext4u_admin_runtime;
GRANT SELECT ON fulfillment.participant_access_targets TO planext4u_admin_runtime;
RESET ROLE;
