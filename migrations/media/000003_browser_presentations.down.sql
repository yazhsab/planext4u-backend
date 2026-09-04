SET ROLE planext4u_media_owner;

DROP INDEX IF EXISTS media.media_assets_public_presentation_idx;
ALTER TABLE media.assets DROP CONSTRAINT IF EXISTS media_assets_dimensions_valid;
ALTER TABLE media.assets DROP CONSTRAINT IF EXISTS media_assets_alt_text_bounded;
ALTER TABLE media.assets DROP COLUMN IF EXISTS presentation_height;
ALTER TABLE media.assets DROP COLUMN IF EXISTS presentation_width;
ALTER TABLE media.assets DROP COLUMN IF EXISTS alt_text;

RESET ROLE;
