SET ROLE planext4u_supply_owner;
ALTER TABLE supply.application_submissions
 DROP COLUMN contact_name,
 DROP COLUMN business_type,
 DROP COLUMN business_name;
RESET ROLE;
