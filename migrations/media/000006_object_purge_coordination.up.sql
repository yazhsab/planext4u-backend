BEGIN;
SET ROLE planext4u_media_owner;

-- This is coordination evidence, not a terminal account-erasure receipt.
-- The ordinary media runtime may read it but cannot declare a version purge.
CREATE TABLE media.object_purge_state (
    asset_id uuid PRIMARY KEY REFERENCES media.assets(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    purged_at timestamptz,
    CHECK (purged_at IS NULL OR purged_at >= created_at)
);
REVOKE ALL ON media.object_purge_state FROM PUBLIC;
GRANT SELECT ON media.object_purge_state TO planext4u_media_runtime;

-- Include preexisting tombstones without interpreting their unversioned S3
-- deletes as physical erasure. A future cleanup worker must verify all versions.
INSERT INTO media.object_purge_state (asset_id)
SELECT id FROM media.assets WHERE lifecycle_state = 'DELETED';

CREATE FUNCTION media.require_deleted_asset_for_purge_state() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE
    asset_state text;
BEGIN
    SELECT lifecycle_state INTO asset_state
    FROM media.assets WHERE id = NEW.asset_id FOR UPDATE;
    IF asset_state IS DISTINCT FROM 'DELETED' THEN
        RAISE EXCEPTION 'only a tombstoned asset can enter purge coordination' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.require_deleted_asset_for_purge_state() FROM PUBLIC;
CREATE TRIGGER require_deleted_asset_for_purge_state
    BEFORE INSERT ON media.object_purge_state
    FOR EACH ROW EXECUTE FUNCTION media.require_deleted_asset_for_purge_state();

CREATE FUNCTION media.enqueue_object_purge_state() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.lifecycle_state = 'DELETED' THEN
        RETURN NEW;
    END IF;
    INSERT INTO media.object_purge_state (asset_id) VALUES (NEW.id)
    ON CONFLICT (asset_id) DO NOTHING;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.enqueue_object_purge_state() FROM PUBLIC;
CREATE TRIGGER enqueue_object_purge_state
    AFTER INSERT OR UPDATE OF lifecycle_state ON media.assets
    FOR EACH ROW WHEN (NEW.lifecycle_state = 'DELETED')
    EXECUTE FUNCTION media.enqueue_object_purge_state();

-- An issued grant must never appear after the tombstone or purge decision.
-- Locking the parent asset serializes a queued grant with Tombstone and with
-- the future version-cleanup worker's asset lock.
CREATE FUNCTION media.reject_deleted_asset_upload_session() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE
    asset_state text;
BEGIN
    SELECT lifecycle_state INTO asset_state
    FROM media.assets WHERE id = NEW.asset_id FOR UPDATE;
    IF asset_state IS NULL OR asset_state = 'DELETED' THEN
        RAISE EXCEPTION 'media tombstone forbids new upload grants' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.reject_deleted_asset_upload_session() FROM PUBLIC;
CREATE TRIGGER reject_deleted_asset_upload_session
    BEFORE INSERT ON media.upload_sessions
    FOR EACH ROW EXECUTE FUNCTION media.reject_deleted_asset_upload_session();

CREATE FUNCTION media.protect_object_purge_state() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,media AS $$
DECLARE
    asset_state text;
BEGIN
    IF TG_OP = 'DELETE' OR TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION 'media object purge evidence is retained' USING ERRCODE='23514';
    END IF;
    IF NEW.asset_id IS DISTINCT FROM OLD.asset_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (OLD.purged_at IS NOT NULL AND NEW.purged_at IS DISTINCT FROM OLD.purged_at) THEN
        RAISE EXCEPTION 'media object purge evidence is immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.purged_at IS NOT NULL AND OLD.purged_at IS NULL THEN
        -- UPDATE already locks the purge-state row. Lock the asset next,
        -- matching normal deletion's order, so grant INSERT cannot commit
        -- across a mark decision and no cleanup/delete deadlock is introduced.
        SELECT lifecycle_state INTO asset_state
        FROM media.assets WHERE id = NEW.asset_id FOR UPDATE;
        IF asset_state IS DISTINCT FROM 'DELETED' THEN
            RAISE EXCEPTION 'only a deleted media asset can be marked purged' USING ERRCODE='23514';
        END IF;
        -- UNKNOWN historical grants need a separately reviewed clearance
        -- path. Even a known conditional PUT can race until its signed expiry
        -- plus a conservative clock-skew window has passed.
        IF NOT EXISTS (
            SELECT 1 FROM media.upload_sessions WHERE asset_id = NEW.asset_id
        ) OR EXISTS (
            SELECT 1 FROM media.upload_sessions
            WHERE asset_id = NEW.asset_id
              AND (grant_provenance <> 'AWS_SIGV4_IF_NONE_MATCH'
                   OR grant_valid_until IS NULL
                   OR grant_valid_until > clock_timestamp() - interval '5 minutes')
        ) THEN
            RAISE EXCEPTION 'media upload grant is not closed for version purge' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION media.protect_object_purge_state() FROM PUBLIC;
CREATE TRIGGER protect_object_purge_state_write
    BEFORE UPDATE OR DELETE ON media.object_purge_state
    FOR EACH ROW EXECUTE FUNCTION media.protect_object_purge_state();
CREATE TRIGGER protect_object_purge_state_truncate
    BEFORE TRUNCATE ON media.object_purge_state
    FOR EACH STATEMENT EXECUTE FUNCTION media.protect_object_purge_state();

COMMENT ON TABLE media.object_purge_state IS
    'Cross-process per-asset object cleanup coordination. purged_at requires future exact-key version verification and is not an account-erasure receipt.';

RESET ROLE;
COMMIT;
