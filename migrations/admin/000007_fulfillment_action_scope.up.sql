BEGIN;
SET ROLE planext4u_admin_owner;
-- The admin BFF may read the same bounded decision to present current action
-- controls. Owning commands still recheck it under the organization lock.
GRANT EXECUTE ON FUNCTION admin.fulfillment_territory_allowed(uuid,char,uuid,uuid,text) TO planext4u_admin_runtime;
RESET ROLE;
COMMIT;
