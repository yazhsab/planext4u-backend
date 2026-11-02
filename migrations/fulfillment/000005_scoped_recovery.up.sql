SET ROLE planext4u_fulfillment_owner;
-- Keep legacy tables intact so this is an additive upgrade. New history is
-- scoped to tenant/country/rider, and profiles negotiate these watermarks.
CREATE TABLE fulfillment.rider_command_history (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,rider_identity_id uuid NOT NULL,
 device_sequence bigint NOT NULL,command_id text NOT NULL,kind text NOT NULL,
 status text NOT NULL,result jsonb NOT NULL,processed_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,rider_identity_id,device_sequence),
 UNIQUE(tenant_id,country,rider_identity_id,command_id)
);
CREATE TABLE fulfillment.rider_location_history (
 tenant_id uuid NOT NULL,country char(2) NOT NULL,rider_identity_id uuid NOT NULL,
 sequence bigint NOT NULL,latitude double precision NOT NULL,longitude double precision NOT NULL,
 accuracy_m double precision NOT NULL,updated_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,country,rider_identity_id)
);
DO $$ BEGIN
 IF EXISTS (SELECT c.rider_identity_id FROM fulfillment.offline_commands c LEFT JOIN fulfillment.rider_profiles p ON p.rider_identity_id=c.rider_identity_id GROUP BY c.rider_identity_id HAVING count(DISTINCT p.id)<>1) THEN
  RAISE EXCEPTION 'Offline history needs an unambiguous rider profile before migration';
 END IF;
END $$;
INSERT INTO fulfillment.rider_command_history SELECT p.tenant_id,p.country,c.rider_identity_id,c.device_sequence,c.command_id,c.kind,c.status,c.result,c.processed_at FROM fulfillment.offline_commands c JOIN fulfillment.rider_profiles p ON p.rider_identity_id=c.rider_identity_id;
INSERT INTO fulfillment.rider_location_history SELECT tenant_id,country,rider_identity_id,sequence,latitude,longitude,accuracy_m,updated_at,expires_at FROM fulfillment.rider_locations;
GRANT SELECT,INSERT,UPDATE ON fulfillment.rider_command_history,fulfillment.rider_location_history TO planext4u_fulfillment_runtime;
RESET ROLE;
