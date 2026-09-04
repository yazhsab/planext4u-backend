SET ROLE planext4u_media_owner;

ALTER TABLE media.assets ADD COLUMN alt_text text;
ALTER TABLE media.assets ADD COLUMN presentation_width integer;
ALTER TABLE media.assets ADD COLUMN presentation_height integer;

ALTER TABLE media.assets ADD CONSTRAINT media_assets_alt_text_bounded
    CHECK (alt_text IS NULL OR length(btrim(alt_text)) BETWEEN 1 AND 240);
ALTER TABLE media.assets ADD CONSTRAINT media_assets_dimensions_valid
    CHECK (
        (presentation_width IS NULL AND presentation_height IS NULL)
        OR (presentation_width BETWEEN 1 AND 16384 AND presentation_height BETWEEN 1 AND 16384)
    );

CREATE INDEX media_assets_public_presentation_idx
    ON media.assets (tenant_id, country, id)
    WHERE classification = 'PUBLIC' AND purpose = 'CATALOG_IMAGE' AND lifecycle_state = 'READY';

RESET ROLE;
