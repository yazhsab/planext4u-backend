BEGIN;
SET ROLE planext4u_identity_owner;
CREATE TABLE identity.profile_occupations (
 identity_id text NOT NULL REFERENCES identity.identities(id),
 tenant_id text NOT NULL,country char(2) NOT NULL CHECK(country ~ '^[A-Z]{2}$'),
 occupation jsonb,revision bigint NOT NULL CHECK(revision>0),updated_at timestamptz NOT NULL,
 PRIMARY KEY(identity_id,country),
 CHECK(occupation IS NULL OR jsonb_typeof(occupation)='object')
);
GRANT SELECT,INSERT,UPDATE ON identity.profile_occupations TO planext4u_identity_runtime;
RESET ROLE;
COMMIT;
