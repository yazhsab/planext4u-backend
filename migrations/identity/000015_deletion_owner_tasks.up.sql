SET ROLE planext4u_identity_owner;
-- A durable, account-wide work inventory; READY is a handoff state only.
-- neither this table nor the coordinator performs owner erasure.
CREATE TABLE identity.deletion_owner_tasks (
 request_id text NOT NULL REFERENCES identity.account_deletion_requests(id),
 owner text NOT NULL REFERENCES identity.deletion_owner_requirements(owner),
 status text NOT NULL CHECK(status IN
  ('WAITING_POLICY','WAITING_RETENTION','BLOCKED_HOLD','READY','ACKNOWLEDGED')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 checked_at timestamptz,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(request_id,owner)
);
CREATE INDEX deletion_owner_tasks_status_idx ON identity.deletion_owner_tasks(status,updated_at);
CREATE INDEX deletion_due_sweep_idx ON identity.account_deletion_requests(effective_at,id)
 WHERE status='SCHEDULED';
GRANT SELECT ON identity.deletion_owner_requirements,identity.deletion_retention_policies,
 identity.deletion_owner_receipts,identity.deletion_legal_holds TO planext4u_identity_runtime;
GRANT SELECT,INSERT,UPDATE ON identity.deletion_owner_tasks TO planext4u_identity_runtime;
RESET ROLE;
