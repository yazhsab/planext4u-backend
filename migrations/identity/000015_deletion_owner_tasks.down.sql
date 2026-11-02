SET ROLE planext4u_identity_owner;
DROP INDEX identity.deletion_due_sweep_idx;
DROP TABLE identity.deletion_owner_tasks;
REVOKE SELECT ON identity.deletion_owner_requirements,identity.deletion_retention_policies,
 identity.deletion_owner_receipts,identity.deletion_legal_holds FROM planext4u_identity_runtime;
RESET ROLE;
