SET ROLE planext4u_identity_owner;
DROP TRIGGER account_deletion_completion_guard ON identity.account_deletion_requests;
DROP FUNCTION identity.guard_deletion_completed();
DROP TABLE identity.deletion_legal_holds;
DROP TABLE identity.deletion_owner_receipts;
DROP TABLE identity.deletion_retention_policies;
DROP TABLE identity.deletion_owner_requirements;
RESET ROLE;
