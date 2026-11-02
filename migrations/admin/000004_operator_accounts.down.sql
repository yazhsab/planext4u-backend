BEGIN;
SET ROLE planext4u_admin_owner;
DROP TABLE admin.operator_accounts;
RESET ROLE;
COMMIT;
