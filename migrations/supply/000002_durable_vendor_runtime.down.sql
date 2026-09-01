SET ROLE planext4u_supply_owner;
ALTER TABLE supply.promotions DROP COLUMN IF EXISTS conversions, DROP COLUMN IF EXISTS impressions;
DROP TABLE IF EXISTS supply.application_timeline;
ALTER TABLE supply.vendor_applications DROP COLUMN IF EXISTS bank_ifsc, DROP COLUMN IF EXISTS bank_holder_name;
RESET ROLE;
