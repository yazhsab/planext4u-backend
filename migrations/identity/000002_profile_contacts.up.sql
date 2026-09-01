SET ROLE planext4u_identity_owner;

ALTER TABLE identity.profiles
    ADD COLUMN email text NOT NULL DEFAULT '',
    ADD COLUMN phone text NOT NULL DEFAULT '',
    ADD CONSTRAINT profiles_email_length CHECK (char_length(email) <= 254),
    ADD CONSTRAINT profiles_phone_length CHECK (char_length(phone) <= 20);

GRANT USAGE ON SCHEMA identity TO planext4u_transaction_runtime;
GRANT SELECT (identity_id,display_name,email,phone) ON identity.profiles TO planext4u_transaction_runtime;
GRANT SELECT (id,tenant_id,disabled_at) ON identity.identities TO planext4u_transaction_runtime;

RESET ROLE;
