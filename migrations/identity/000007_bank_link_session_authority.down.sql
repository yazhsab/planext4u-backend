BEGIN;
SET ROLE planext4u_identity_owner;
DROP FUNCTION IF EXISTS identity.lock_bank_link_session(text,char,text,text,boolean);
RESET ROLE;
COMMIT;
