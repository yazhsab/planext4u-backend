SET ROLE planext4u_identity_owner;
DROP FUNCTION IF EXISTS identity.lock_address_session(text,char,text,text);
RESET ROLE;
