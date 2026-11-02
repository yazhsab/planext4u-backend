BEGIN;
SET ROLE planext4u_fulfillment_owner;
-- Explicit, non-personal read model for the organization owner to bind real
-- fulfillment resources. No business table write permission is shared.
CREATE VIEW fulfillment.territory_access_targets AS
 SELECT tenant_id,country,id,name,region_id FROM fulfillment.territories;
GRANT USAGE ON SCHEMA fulfillment TO planext4u_admin_runtime;
GRANT SELECT ON fulfillment.territory_access_targets TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
