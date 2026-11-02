SET ROLE planext4u_supply_owner;

-- The order owner is authoritative for placement. Supply records the exact
-- source intent and fingerprint before exposing vendor work. This ledger is
-- append-only for runtime roles so retries cannot rewrite an accepted order.
CREATE TABLE supply.order_work_ingestions (
    source_intent_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    order_id uuid NOT NULL,
    vendor_identity_id uuid NOT NULL,
    order_revision bigint NOT NULL CHECK (order_revision > 0),
    payload_fingerprint char(64) NOT NULL CHECK (payload_fingerprint ~ '^[0-9a-f]{64}$'),
    source_payload jsonb NOT NULL CHECK (jsonb_typeof(source_payload) = 'object'),
    charge_minor bigint NOT NULL CHECK (charge_minor >= 0),
    charge_currency char(3) NOT NULL CHECK (charge_currency ~ '^[A-Z]{3}$'),
    work_item_id uuid NOT NULL UNIQUE REFERENCES supply.work_items(id),
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, order_id, vendor_identity_id)
);
CREATE INDEX supply_order_work_reconcile_idx
    ON supply.order_work_ingestions (tenant_id, country, created_at);

-- Runtime roles cannot change the ledger directly. This function is the only
-- insertion path and checks vendor approval in the same transaction as work.
CREATE FUNCTION supply.ingest_order_work_intent(
    source_id uuid, tenant uuid, home_country char(2), customer uuid,
    source_order uuid, vendor uuid, source_revision bigint,
    source_fingerprint char(64), payload jsonb, charge_minor bigint,
    charge_currency char(3), occurred_at timestamptz
) RETURNS TABLE(work_id uuid, was_replayed boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, supply AS $$
DECLARE
    existing supply.order_work_ingestions%ROWTYPE;
    applicant_status text;
    applicant_verified boolean;
    created_work uuid;
BEGIN
    IF source_revision < 1 OR charge_minor < 0 OR payload IS NULL OR
       jsonb_typeof(payload) <> 'object' OR
       payload->>'intent_id' IS DISTINCT FROM source_id::text OR
       payload->>'tenant_id' IS DISTINCT FROM tenant::text OR
       payload->>'country' IS DISTINCT FROM home_country::text OR
       payload->>'customer_id' IS DISTINCT FROM customer::text OR
       payload->>'order_id' IS DISTINCT FROM source_order::text OR
       payload->>'vendor_id' IS DISTINCT FROM vendor::text OR
       payload->>'order_revision' IS DISTINCT FROM source_revision::text OR
       payload->>'currency' IS DISTINCT FROM charge_currency::text OR
       charge_currency !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'invalid order work source' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(source_id::text, 0));
    SELECT * INTO existing FROM supply.order_work_ingestions
    WHERE source_intent_id = source_id;
    IF FOUND THEN
        IF existing.tenant_id <> tenant OR existing.country <> home_country OR
           existing.customer_identity_id <> customer OR existing.order_id <> source_order OR
           existing.vendor_identity_id <> vendor OR existing.order_revision <> source_revision OR
           existing.payload_fingerprint <> source_fingerprint OR existing.source_payload <> payload OR
           existing.charge_minor <> charge_minor OR existing.charge_currency <> charge_currency THEN
            RAISE EXCEPTION 'order work source changed' USING ERRCODE = 'PX101';
        END IF;
        RETURN QUERY SELECT existing.work_item_id, true;
        RETURN;
    END IF;
    SELECT status,verified INTO applicant_status,applicant_verified
    FROM supply.vendor_applications
    WHERE tenant_id=tenant AND country=home_country AND vendor_identity_id=vendor
    FOR SHARE;
    IF NOT FOUND OR applicant_status <> 'APPROVED' OR NOT applicant_verified THEN
        RAISE EXCEPTION 'vendor is not active' USING ERRCODE = 'PX102';
    END IF;
    created_work := gen_random_uuid();
    INSERT INTO supply.work_items
        (id,tenant_id,country,vendor_identity_id,reference_type,reference_id,
         status,total_minor,currency,customer_label,updated_at)
    VALUES (created_work,tenant,home_country,vendor,'MARKETPLACE_ORDER',source_order,
            'NEW',charge_minor,charge_currency,'Customer',occurred_at);
    INSERT INTO supply.order_work_ingestions
        (source_intent_id,tenant_id,country,customer_identity_id,order_id,
         vendor_identity_id,order_revision,payload_fingerprint,source_payload,
         charge_minor,charge_currency,work_item_id,created_at)
    VALUES (source_id,tenant,home_country,customer,source_order,
            vendor,source_revision,source_fingerprint,payload,charge_minor,
            charge_currency,created_work,occurred_at);
    RETURN QUERY SELECT created_work, false;
END $$;
REVOKE ALL ON FUNCTION supply.ingest_order_work_intent(uuid,uuid,char(2),uuid,uuid,uuid,bigint,char(64),jsonb,bigint,char(3),timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION supply.ingest_order_work_intent(uuid,uuid,char(2),uuid,uuid,uuid,bigint,char(64),jsonb,bigint,char(3),timestamptz) TO planext4u_transaction_runtime;
RESET ROLE;
