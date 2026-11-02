SET ROLE planext4u_media_owner;
DROP TRIGGER IF EXISTS protect_claimed_identity_evidence ON media.assets;
DROP FUNCTION IF EXISTS media.protect_claimed_identity_evidence();
DROP FUNCTION IF EXISTS media.claim_vendor_identity_evidence(uuid,char,text,text,uuid[]);
DROP FUNCTION IF EXISTS media.claim_rider_identity_evidence(uuid,char,text,text,uuid[]);
DROP FUNCTION IF EXISTS media.claim_identity_evidence(uuid,char,text,text,text,uuid[]);
DROP TABLE IF EXISTS media.identity_evidence_claims;
RESET ROLE;
