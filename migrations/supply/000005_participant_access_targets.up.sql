SET ROLE planext4u_supply_owner;
CREATE VIEW supply.participant_access_targets AS SELECT tenant_id,country,vendor_identity_id AS id FROM supply.vendor_applications;
GRANT USAGE ON SCHEMA supply TO planext4u_admin_runtime;
GRANT SELECT ON supply.participant_access_targets TO planext4u_admin_runtime;
RESET ROLE;
