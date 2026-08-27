CREATE SCHEMA IF NOT EXISTS media AUTHORIZATION planext4u_media_owner;
ALTER SCHEMA media OWNER TO planext4u_media_owner;
REVOKE ALL ON SCHEMA media FROM PUBLIC;
SET ROLE planext4u_media_owner;

CREATE TABLE media.assets (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    owner_id text NOT NULL,
    object_key text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 52428800),
    checksum_sha256 char(64) NOT NULL CHECK (checksum_sha256 ~ '^[a-f0-9]{64}$'),
    status text NOT NULL CHECK (status IN ('PENDING', 'READY', 'QUARANTINED', 'DELETED')),
    classification text NOT NULL CHECK (classification IN ('PUBLIC', 'INTERNAL', 'CONFIDENTIAL', 'RESTRICTED')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, object_key)
);
CREATE INDEX media_assets_owner_idx ON media.assets (tenant_id, owner_id, status, created_at DESC);

CREATE TABLE media.upload_sessions (
    id uuid PRIMARY KEY,
    asset_id uuid NOT NULL REFERENCES media.assets(id),
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    created_at timestamptz NOT NULL
);

COMMENT ON COLUMN media.assets.object_key IS 'Opaque storage key only; signed URLs and provider credentials are prohibited.';
GRANT USAGE ON SCHEMA media TO planext4u_media_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA media TO planext4u_media_runtime;
RESET ROLE;
