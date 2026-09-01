SET ROLE planext4u_food_owner;
DROP INDEX IF EXISTS food.food_pending_acceptance_idx;
DROP INDEX IF EXISTS food.food_order_source_cart_idx;
ALTER TABLE food.orders DROP COLUMN IF EXISTS wallet_debit_entry_id, DROP COLUMN IF EXISTS refund_state, DROP COLUMN IF EXISTS payment_method, DROP COLUMN IF EXISTS postal_code, DROP COLUMN IF EXISTS source_cart_id;
DROP TABLE IF EXISTS food.policies;
ALTER TABLE food.option_groups DROP COLUMN IF EXISTS instructions;
RESET ROLE;
