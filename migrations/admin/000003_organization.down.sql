BEGIN;
SET ROLE planext4u_admin_owner;
DROP TABLE admin.organization_events;
DROP TABLE admin.staff_invitations;
DROP TABLE admin.staff_assignments;
DROP TABLE admin.job_roles;
DROP TABLE admin.organization_nodes;
RESET ROLE;
COMMIT;
