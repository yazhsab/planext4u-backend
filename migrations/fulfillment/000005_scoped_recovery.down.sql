SET ROLE planext4u_fulfillment_owner;
-- Refuse a lossy downgrade once the scoped protocol has acknowledged changes.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM fulfillment.rider_command_history n WHERE NOT EXISTS(SELECT 1 FROM fulfillment.offline_commands o WHERE o.rider_identity_id=n.rider_identity_id AND o.command_id=n.command_id AND o.device_sequence=n.device_sequence)) OR EXISTS (SELECT 1 FROM fulfillment.rider_location_history n WHERE NOT EXISTS(SELECT 1 FROM fulfillment.rider_locations o WHERE o.rider_identity_id=n.rider_identity_id AND o.sequence=n.sequence AND o.tenant_id=n.tenant_id AND o.country=n.country)) THEN
  RAISE EXCEPTION 'Scoped recovery has new acknowledgements; preserve history and roll forward';
 END IF;
END $$;
DROP TABLE fulfillment.rider_command_history;
DROP TABLE fulfillment.rider_location_history;
RESET ROLE;
