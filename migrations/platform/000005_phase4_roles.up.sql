DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_supply_owner') THEN CREATE ROLE planext4u_supply_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_supply_runtime') THEN CREATE ROLE planext4u_supply_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_food_owner') THEN CREATE ROLE planext4u_food_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_food_runtime') THEN CREATE ROLE planext4u_food_runtime NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_fulfillment_owner') THEN CREATE ROLE planext4u_fulfillment_owner NOLOGIN; END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'planext4u_fulfillment_runtime') THEN CREATE ROLE planext4u_fulfillment_runtime NOLOGIN; END IF;
END $$;

GRANT planext4u_supply_owner TO CURRENT_USER;
GRANT planext4u_food_owner TO CURRENT_USER;
GRANT planext4u_fulfillment_owner TO CURRENT_USER;
