SET ROLE planext4u_identity_owner;

REVOKE SELECT (id,tenant_id,disabled_at) ON identity.identities FROM planext4u_transaction_runtime;
REVOKE SELECT (identity_id,display_name,email,phone) ON identity.profiles FROM planext4u_transaction_runtime;
REVOKE USAGE ON SCHEMA identity FROM planext4u_transaction_runtime;

ALTER TABLE identity.profiles
    DROP CONSTRAINT IF EXISTS profiles_phone_length,
    DROP CONSTRAINT IF EXISTS profiles_email_length,
    DROP COLUMN IF EXISTS phone,
    DROP COLUMN IF EXISTS email;

RESET ROLE;
