SET ROLE planext4u_supply_owner;

CREATE TABLE supply.reviewer_evidence_access (
 id uuid PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
 reviewer_identity_id uuid NOT NULL,
 vendor_identity_id uuid NOT NULL,
 document_kind text NOT NULL,
 outcome text NOT NULL CHECK (outcome IN ('GRANTED','DENIED')),
 occurred_at timestamptz NOT NULL
);
CREATE INDEX supply_reviewer_evidence_access_lookup ON supply.reviewer_evidence_access
 (tenant_id,country,vendor_identity_id,occurred_at DESC);
REVOKE ALL ON supply.reviewer_evidence_access FROM PUBLIC;
GRANT SELECT,INSERT ON supply.reviewer_evidence_access TO planext4u_supply_runtime;

RESET ROLE;
