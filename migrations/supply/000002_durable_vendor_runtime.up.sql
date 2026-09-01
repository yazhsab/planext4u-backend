SET ROLE planext4u_supply_owner;

ALTER TABLE supply.vendor_applications
    ADD COLUMN bank_holder_name text,
    ADD COLUMN bank_ifsc text;

CREATE TABLE supply.application_timeline (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id uuid NOT NULL REFERENCES supply.vendor_applications(id) ON DELETE CASCADE,
    status text NOT NULL,
    actor_subject_id uuid NOT NULL,
    reason text,
    created_at timestamptz NOT NULL
);
CREATE INDEX supply_application_timeline_idx ON supply.application_timeline (application_id,id);

ALTER TABLE supply.promotions
    ADD COLUMN impressions bigint NOT NULL DEFAULT 0 CHECK (impressions >= 0),
    ADD COLUMN conversions bigint NOT NULL DEFAULT 0 CHECK (conversions >= 0);

GRANT SELECT, INSERT, UPDATE, DELETE ON supply.application_timeline TO planext4u_supply_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA supply TO planext4u_supply_runtime;
RESET ROLE;
