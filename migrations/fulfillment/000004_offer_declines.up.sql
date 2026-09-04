SET ROLE planext4u_fulfillment_owner;

CREATE TABLE fulfillment.offer_declines (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    task_id uuid NOT NULL REFERENCES fulfillment.delivery_tasks(id) ON DELETE CASCADE,
    rider_identity_id uuid NOT NULL,
    task_revision bigint NOT NULL CHECK (task_revision > 0),
    reason_code text NOT NULL CHECK (reason_code IN ('TOO_FAR','VEHICLE_OR_CAPACITY','ENDING_DUTY','SAFETY_CONCERN','OTHER')),
    note text CHECK (note IS NULL OR char_length(note) BETWEEN 1 AND 240),
    declined_at timestamptz NOT NULL,
    PRIMARY KEY (task_id,rider_identity_id)
);

CREATE INDEX fulfillment_offer_declines_rider_idx
    ON fulfillment.offer_declines (tenant_id,country,rider_identity_id,declined_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON fulfillment.offer_declines TO planext4u_fulfillment_runtime;
RESET ROLE;
