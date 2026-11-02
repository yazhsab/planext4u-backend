SET ROLE planext4u_supply_owner;
DROP FUNCTION IF EXISTS supply.ingest_order_work_intent(uuid,uuid,char(2),uuid,uuid,uuid,bigint,char(64),jsonb,bigint,char(3),timestamptz);
DROP TABLE IF EXISTS supply.order_work_ingestions;
RESET ROLE;
