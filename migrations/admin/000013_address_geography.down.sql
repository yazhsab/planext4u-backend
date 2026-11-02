SET ROLE planext4u_admin_owner;
DROP FUNCTION IF EXISTS admin.active_geography(uuid,char,text,uuid,text,uuid);
DROP FUNCTION IF EXISTS admin.lock_address_geography(uuid,char,uuid);
RESET ROLE;
