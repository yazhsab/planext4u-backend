SET ROLE planext4u_identity_owner;
REVOKE EXECUTE ON FUNCTION identity.privacy_media_erasure_country_scope(text)
 FROM planext4u_media_owner;
DROP FUNCTION identity.privacy_media_erasure_country_scope(text);
REVOKE USAGE ON SCHEMA identity FROM planext4u_media_owner;
RESET ROLE;
