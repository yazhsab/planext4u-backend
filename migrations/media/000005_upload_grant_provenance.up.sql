BEGIN;
SET ROLE planext4u_media_owner;

-- Existing grants are deliberately unknown. A later version purge must not
-- infer that historical signed PUTs were conditional or already expired.
ALTER TABLE media.upload_sessions
    ADD COLUMN grant_provenance text NOT NULL DEFAULT 'UNKNOWN'
        CHECK (grant_provenance IN ('UNKNOWN', 'AWS_SIGV4_IF_NONE_MATCH')),
    ADD COLUMN grant_valid_until timestamptz,
    ADD CONSTRAINT media_upload_grant_provenance_complete
        CHECK (
            (grant_provenance = 'UNKNOWN' AND grant_valid_until IS NULL)
            OR (grant_provenance = 'AWS_SIGV4_IF_NONE_MATCH' AND grant_valid_until IS NOT NULL)
        );

CREATE FUNCTION media.protect_upload_grant_provenance() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
BEGIN
    IF OLD.grant_provenance IS DISTINCT FROM NEW.grant_provenance
       OR OLD.grant_valid_until IS DISTINCT FROM NEW.grant_valid_until
       OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
       OR OLD.asset_id IS DISTINCT FROM NEW.asset_id
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'media upload grant provenance is immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.completed_at IS NOT NULL AND OLD.completed_at IS DISTINCT FROM NEW.completed_at THEN
        RAISE EXCEPTION 'media upload completion is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.protect_upload_grant_provenance() FROM PUBLIC;
CREATE TRIGGER protect_upload_grant_provenance
    BEFORE UPDATE ON media.upload_sessions
    FOR EACH ROW EXECUTE FUNCTION media.protect_upload_grant_provenance();

CREATE FUNCTION media.protect_deleted_asset() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
BEGIN
    IF OLD.lifecycle_state = 'DELETED'
       AND (NEW.lifecycle_state <> 'DELETED' OR NEW.status <> 'DELETED') THEN
        RAISE EXCEPTION 'media tombstone is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.protect_deleted_asset() FROM PUBLIC;
CREATE TRIGGER protect_deleted_asset
    BEFORE UPDATE ON media.assets
    FOR EACH ROW EXECUTE FUNCTION media.protect_deleted_asset();

-- The media runtime may transition lifecycle state and complete an upload
-- session; it cannot rewrite subject/key/grant scope or delete the evidence.
REVOKE UPDATE, DELETE ON media.assets FROM planext4u_media_runtime;
GRANT UPDATE (status, lifecycle_state, ready_at, rejected_code, version, updated_at)
    ON media.assets TO planext4u_media_runtime;
REVOKE UPDATE, DELETE ON media.upload_sessions FROM planext4u_media_runtime;
GRANT UPDATE (completed_at) ON media.upload_sessions TO planext4u_media_runtime;

COMMENT ON COLUMN media.upload_sessions.grant_provenance IS
    'UNKNOWN for historical/unproved grants; only structurally verified SigV4 conditional PUT grants are classified.';
COMMENT ON COLUMN media.upload_sessions.grant_valid_until IS
    'Signed X-Amz-Date plus X-Amz-Expires, which may be later than the service upload expiry.';

RESET ROLE;
COMMIT;
