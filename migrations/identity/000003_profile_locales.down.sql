SET ROLE planext4u_identity_owner;

UPDATE identity.profiles
SET locale = 'en', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE locale NOT IN ('en', 'ta');

ALTER TABLE identity.profiles
    DROP CONSTRAINT profiles_locale_allowed,
    ADD CONSTRAINT profiles_locale_allowed CHECK (locale IN ('en', 'ta'));

RESET ROLE;
