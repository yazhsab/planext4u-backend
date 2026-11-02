BEGIN;
SET ROLE planext4u_media_owner;

-- Never discard issued grant evidence or re-open the old broad runtime writes
-- while assets still exist. Fresh empty-schema rollback remains reversible.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media.upload_sessions)
       OR EXISTS (SELECT 1 FROM media.assets) THEN
        RAISE EXCEPTION 'media asset or upload grant evidence prevents rollback' USING ERRCODE='23514';
    END IF;
END $$;
DROP TRIGGER protect_deleted_asset ON media.assets;
DROP FUNCTION media.protect_deleted_asset();
DROP TRIGGER protect_upload_grant_provenance ON media.upload_sessions;
DROP FUNCTION media.protect_upload_grant_provenance();
ALTER TABLE media.upload_sessions DROP CONSTRAINT media_upload_grant_provenance_complete;
ALTER TABLE media.upload_sessions
    DROP COLUMN grant_valid_until,
    DROP COLUMN grant_provenance;
GRANT UPDATE, DELETE ON media.assets TO planext4u_media_runtime;
GRANT UPDATE, DELETE ON media.upload_sessions TO planext4u_media_runtime;

RESET ROLE;
COMMIT;
