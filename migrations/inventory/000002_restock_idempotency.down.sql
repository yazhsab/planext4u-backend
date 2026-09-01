SET ROLE planext4u_inventory_owner;
DROP TABLE IF EXISTS inventory.restock_requests;
ALTER TABLE inventory.reservations
    DROP CONSTRAINT IF EXISTS inventory_reservation_response_object,
    DROP COLUMN IF EXISTS response_payload;
ALTER TABLE inventory.reservation_lines DROP COLUMN IF EXISTS restocked_quantity;
RESET ROLE;
