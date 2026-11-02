SET ROLE planext4u_commerce_owner;
CREATE TABLE commerce.checkout_workflows (
 id text PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 customer_id uuid NOT NULL,
 idempotency_key text NOT NULL,
 fingerprint text NOT NULL,
 payload jsonb NOT NULL,
 done boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL,
 UNIQUE (tenant_id,country,customer_id,idempotency_key)
);
CREATE INDEX checkout_workflow_pending_idx ON commerce.checkout_workflows(updated_at) WHERE done=false;
GRANT SELECT,INSERT,UPDATE ON commerce.checkout_workflows TO planext4u_commerce_runtime;
RESET ROLE;
