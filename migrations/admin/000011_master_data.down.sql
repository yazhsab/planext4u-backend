BEGIN;
SET ROLE planext4u_admin_owner;
DROP TABLE admin.master_data_versions;
DROP TABLE admin.master_data;
RESET ROLE;
COMMIT;
