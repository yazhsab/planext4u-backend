CREATE SCHEMA IF NOT EXISTS bankverification AUTHORIZATION planext4u_bankverification_owner;
ALTER SCHEMA bankverification OWNER TO planext4u_bankverification_owner;
REVOKE ALL ON SCHEMA bankverification FROM PUBLIC;
SET ROLE planext4u_bankverification_owner;

CREATE TABLE bankverification.link_attempts (
 id uuid PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 owner_id uuid NOT NULL,
 application_role text NOT NULL CHECK(application_role IN ('VENDOR','RIDER')),
 idempotency_hash char(64) NOT NULL CHECK(idempotency_hash ~ '^[a-f0-9]{64}$'),
 status text NOT NULL CHECK(status IN ('CREATING','AWAITING_USER','VERIFYING','VERIFIED','DECLINED','CANCELLED','EXPIRED')),
 provider_session_id text CHECK(provider_session_id ~ '^[A-Za-z0-9_-]{16,160}$'),
 account jsonb,
 verification_id text,
 verified_at timestamptz,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 UNIQUE(tenant_id,country,owner_id,application_role,idempotency_hash),
 CHECK((status='VERIFIED' AND account IS NOT NULL AND verification_id IS NOT NULL AND verified_at IS NOT NULL) OR
       (status<>'VERIFIED' AND account IS NULL AND verification_id IS NULL AND verified_at IS NULL))
);
CREATE UNIQUE INDEX link_attempts_one_active ON bankverification.link_attempts(tenant_id,country,owner_id,application_role)
 WHERE status IN ('CREATING','AWAITING_USER','VERIFYING','VERIFIED');
CREATE INDEX link_attempts_expiry ON bankverification.link_attempts(expires_at) WHERE status IN ('CREATING','AWAITING_USER','VERIFYING','VERIFIED');

CREATE TABLE bankverification.audit_events (
 sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id uuid NOT NULL,
 country char(2) NOT NULL,
 owner_id uuid NOT NULL,
 session_id text NOT NULL,
 attempt_id uuid NOT NULL REFERENCES bankverification.link_attempts(id),
 action text NOT NULL CHECK(action IN ('CREATED','RESUMED','RESOLVED','CANCELLED','EXPIRED')),
 status text NOT NULL,
 occurred_at timestamptz NOT NULL
);
CREATE FUNCTION bankverification.reject_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'bank link audit is append only'; END $$;
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON bankverification.audit_events
 FOR EACH STATEMENT EXECUTE FUNCTION bankverification.reject_audit_mutation();
COMMENT ON COLUMN bankverification.link_attempts.account IS 'Provider-verified masked reference only; no raw account numbers, credentials or hosted launch URLs.';
GRANT USAGE ON SCHEMA bankverification TO planext4u_bankverification_runtime;
GRANT SELECT,INSERT,UPDATE ON bankverification.link_attempts TO planext4u_bankverification_runtime;
GRANT SELECT,INSERT ON bankverification.audit_events TO planext4u_bankverification_runtime;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA bankverification TO planext4u_bankverification_runtime;
RESET ROLE;
