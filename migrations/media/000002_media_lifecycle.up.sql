SET ROLE planext4u_media_owner;

ALTER TABLE media.assets ADD COLUMN purpose text;
ALTER TABLE media.assets ADD COLUMN lifecycle_state text;
ALTER TABLE media.assets ADD COLUMN upload_expires_at timestamptz;
ALTER TABLE media.assets ADD COLUMN ready_at timestamptz;
ALTER TABLE media.assets ADD COLUMN rejected_code text;
ALTER TABLE media.assets ADD COLUMN version bigint;

UPDATE media.assets
SET purpose = CASE classification
        WHEN 'RESTRICTED' THEN 'IDENTITY_DOCUMENT'
        WHEN 'CONFIDENTIAL' THEN 'COMPLETION_PROOF'
        ELSE 'CATALOG_IMAGE'
    END,
    lifecycle_state = CASE status
        WHEN 'READY' THEN 'READY'
        WHEN 'QUARANTINED' THEN 'REJECTED'
        WHEN 'DELETED' THEN 'DELETED'
        ELSE 'PENDING_UPLOAD'
    END,
    upload_expires_at = created_at + interval '10 minutes',
    ready_at = CASE WHEN status = 'READY' THEN updated_at END,
    version = 1;

ALTER TABLE media.assets ADD CONSTRAINT media_assets_purpose_valid
    CHECK (purpose IN ('AVATAR', 'CATALOG_IMAGE', 'COMPLETION_PROOF', 'IDENTITY_DOCUMENT'));
ALTER TABLE media.assets ADD CONSTRAINT media_assets_lifecycle_state_valid
    CHECK (lifecycle_state IN ('PENDING_UPLOAD', 'PENDING_SCAN', 'READY', 'REJECTED', 'EXPIRED', 'DELETED'));
ALTER TABLE media.assets ADD CONSTRAINT media_assets_lifecycle_fields_present
    CHECK (purpose IS NOT NULL AND lifecycle_state IS NOT NULL AND upload_expires_at IS NOT NULL AND version IS NOT NULL) NOT VALID;
ALTER TABLE media.assets VALIDATE CONSTRAINT media_assets_lifecycle_fields_present;
ALTER TABLE media.assets ADD CONSTRAINT media_assets_version_positive CHECK (version >= 1);
ALTER TABLE media.assets ADD CONSTRAINT media_assets_rejected_code_safe
    CHECK (rejected_code IS NULL OR rejected_code ~ '^[A-Z][A-Z0-9_]{2,63}$');

CREATE INDEX media_assets_expiry_idx
    ON media.assets (upload_expires_at)
    WHERE lifecycle_state IN ('PENDING_UPLOAD', 'PENDING_SCAN');

RESET ROLE;
