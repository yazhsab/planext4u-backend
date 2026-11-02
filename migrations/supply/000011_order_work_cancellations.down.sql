SET ROLE planext4u_supply_owner;
DROP FUNCTION IF EXISTS supply.ingest_order_work_cancellation(uuid,uuid,uuid,char(2),uuid,uuid,uuid,bigint,text,char(64),jsonb,timestamptz);
DROP TRIGGER IF EXISTS reject_cancelled_order_work ON supply.order_work_ingestions;
DROP FUNCTION IF EXISTS supply.reject_cancelled_order_work();
DROP TABLE IF EXISTS supply.order_work_cancellations;
RESET ROLE;
