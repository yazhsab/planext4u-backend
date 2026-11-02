BEGIN;
SET ROLE planext4u_identity_owner;
CREATE TABLE identity.account_restrictions (
    identity_id text PRIMARY KEY REFERENCES identity.identities(id),
    source text NOT NULL CHECK (source IN ('USER_DEACTIVATED','SECURITY_DISABLED','DELETED','ACTIVE')),
    changed_at timestamptz NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);
GRANT SELECT,INSERT,UPDATE ON identity.account_restrictions TO planext4u_identity_runtime;
RESET ROLE;
COMMIT;
