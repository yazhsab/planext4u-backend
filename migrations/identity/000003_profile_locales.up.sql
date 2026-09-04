SET ROLE planext4u_identity_owner;

ALTER TABLE identity.profiles
    DROP CONSTRAINT profiles_locale_allowed,
    ADD CONSTRAINT profiles_locale_allowed CHECK (
        locale IN ('en', 'ta', 'hi', 'te', 'kn', 'ml', 'mr', 'bn', 'gu')
    );

RESET ROLE;
