BEGIN;
SET ROLE planext4u_admin_owner;
ALTER TABLE admin.operator_accounts DROP CONSTRAINT operator_accounts_roles_check;
ALTER TABLE admin.operator_accounts ADD CONSTRAINT operator_accounts_roles_check
    CHECK (cardinality(roles) BETWEEN 1 AND 8
       AND roles <@ ARRAY['SUPER_ADMIN','COUNTRY_ADMIN','CONTENT_ADMIN','SUPPORT_ADMIN','AUDITOR','REGIONAL_STAFF']::text[]);
RESET ROLE;
COMMIT;
