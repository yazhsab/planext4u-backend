SET ROLE planext4u_supply_owner;
ALTER TABLE supply.application_submissions
 ADD COLUMN business_name text,
 ADD COLUMN business_type text,
 ADD COLUMN contact_name text;
RESET ROLE;
