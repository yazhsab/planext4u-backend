BEGIN;
SET ROLE planext4u_media_owner;

-- Existing tombstones may have live noncurrent bytes; never discard the
-- coordination row after one has been enqueued.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM media.object_purge_state) THEN
        RAISE EXCEPTION 'media object purge evidence prevents rollback' USING ERRCODE='23514';
    END IF;
END $$;

DROP TRIGGER protect_object_purge_state_truncate ON media.object_purge_state;
DROP TRIGGER protect_object_purge_state_write ON media.object_purge_state;
DROP FUNCTION media.protect_object_purge_state();
DROP TRIGGER enqueue_object_purge_state ON media.assets;
DROP FUNCTION media.enqueue_object_purge_state();
DROP TRIGGER reject_deleted_asset_upload_session ON media.upload_sessions;
DROP FUNCTION media.reject_deleted_asset_upload_session();
DROP TRIGGER require_deleted_asset_for_purge_state ON media.object_purge_state;
DROP FUNCTION media.require_deleted_asset_for_purge_state();
DROP TABLE media.object_purge_state;

RESET ROLE;
COMMIT;
