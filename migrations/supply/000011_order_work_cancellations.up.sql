SET ROLE planext4u_supply_owner;

-- Each ordering cancellation event is recorded without rewriting the original
-- placement ledger. A pre-placement event is a durable tombstone for the
-- deterministic order/vendor source work ID.
CREATE TABLE supply.order_work_cancellations (
    source_cancellation_id uuid PRIMARY KEY,
    source_work_intent_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    customer_identity_id uuid NOT NULL,
    order_id uuid NOT NULL,
    vendor_identity_id uuid NOT NULL,
    order_revision bigint NOT NULL CHECK (order_revision > 0),
    order_status text NOT NULL CHECK (order_status IN ('CANCEL_REQUESTED','CANCELLED')),
    payload_fingerprint char(64) NOT NULL CHECK (payload_fingerprint ~ '^[0-9a-f]{64}$'),
    source_payload jsonb NOT NULL CHECK (jsonb_typeof(source_payload)='object'),
    outcome text NOT NULL CHECK (outcome IN
        ('NO_WORK','CANCELLED','ALREADY_CANCELLED','ALREADY_COMPLETED','ALREADY_REJECTED')),
    work_item_id uuid REFERENCES supply.work_items(id),
    created_at timestamptz NOT NULL,
    UNIQUE (source_work_intent_id,order_revision),
    CHECK ((outcome='NO_WORK') = (work_item_id IS NULL))
);
CREATE INDEX supply_order_work_cancel_reconcile_idx
    ON supply.order_work_cancellations (tenant_id,country,order_id,vendor_identity_id,created_at);
CREATE INDEX supply_order_work_cancel_source_idx
    ON supply.order_work_cancellations (source_work_intent_id);

-- Work ingestion already takes this advisory key. The trigger closes the
-- pre-cancellation path even if a placement worker replays after cancellation.
CREATE FUNCTION supply.reject_cancelled_order_work() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.source_intent_id::text,0));
    IF EXISTS (
        SELECT 1 FROM supply.order_work_cancellations
        WHERE source_work_intent_id=NEW.source_intent_id
    ) THEN
        RAISE EXCEPTION 'order vendor work was cancelled' USING ERRCODE='PX103';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION supply.reject_cancelled_order_work() FROM PUBLIC;
CREATE TRIGGER reject_cancelled_order_work
    BEFORE INSERT ON supply.order_work_ingestions
    FOR EACH ROW EXECUTE FUNCTION supply.reject_cancelled_order_work();

-- Only the supply owner may change work. Runtime callers can invoke this
-- function but cannot directly insert a cancellation receipt.
CREATE FUNCTION supply.ingest_order_work_cancellation(
    source_cancel_id uuid,source_work_id uuid,tenant uuid,home_country char(2),
    customer uuid,source_order uuid,vendor uuid,source_revision bigint,
    source_status text,source_fingerprint char(64),payload jsonb,occurred_at timestamptz
) RETURNS TABLE(work_id uuid,cancel_outcome text,was_replayed boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,supply AS $$
DECLARE
    existing supply.order_work_cancellations%ROWTYPE;
    prior supply.order_work_cancellations%ROWTYPE;
    source_work supply.order_work_ingestions%ROWTYPE;
    current_work supply.work_items%ROWTYPE;
    result_outcome text;
    result_work_id uuid;
BEGIN
    IF source_revision < 1 OR source_status NOT IN ('CANCEL_REQUESTED','CANCELLED') OR
       payload IS NULL OR jsonb_typeof(payload)<>'object' OR
       payload->>'id' IS DISTINCT FROM source_cancel_id::text OR
       payload->>'source_work_intent_id' IS DISTINCT FROM source_work_id::text OR
       payload->>'tenant_id' IS DISTINCT FROM tenant::text OR
       payload->>'country' IS DISTINCT FROM home_country::text OR
       payload->>'customer_id' IS DISTINCT FROM customer::text OR
       payload->>'order_id' IS DISTINCT FROM source_order::text OR
       payload->>'vendor_id' IS DISTINCT FROM vendor::text OR
       payload->>'order_revision' IS DISTINCT FROM source_revision::text OR
       payload->>'order_status' IS DISTINCT FROM source_status THEN
        RAISE EXCEPTION 'invalid order work cancellation' USING ERRCODE='22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(source_work_id::text,0));
    SELECT * INTO existing FROM supply.order_work_cancellations
    WHERE source_cancellation_id=source_cancel_id;
    IF FOUND THEN
        IF existing.source_work_intent_id<>source_work_id OR
           existing.tenant_id<>tenant OR existing.country<>home_country OR
           existing.customer_identity_id<>customer OR existing.order_id<>source_order OR
           existing.vendor_identity_id<>vendor OR existing.order_revision<>source_revision OR
           existing.order_status<>source_status OR
           existing.payload_fingerprint<>source_fingerprint OR existing.source_payload<>payload THEN
            RAISE EXCEPTION 'order work cancellation source changed' USING ERRCODE='PX101';
        END IF;
        RETURN QUERY SELECT existing.work_item_id,existing.outcome,true;
        RETURN;
    END IF;
    SELECT * INTO prior FROM supply.order_work_cancellations
    WHERE source_work_intent_id=source_work_id ORDER BY created_at LIMIT 1;
    IF FOUND AND (prior.tenant_id<>tenant OR prior.country<>home_country OR
                  prior.customer_identity_id<>customer OR prior.order_id<>source_order OR
                  prior.vendor_identity_id<>vendor) THEN
        RAISE EXCEPTION 'order work cancellation scope changed' USING ERRCODE='PX101';
    END IF;
    SELECT * INTO source_work FROM supply.order_work_ingestions
    WHERE source_intent_id=source_work_id;
    IF FOUND AND (source_work.tenant_id<>tenant OR source_work.country<>home_country OR
                  source_work.customer_identity_id<>customer OR source_work.order_id<>source_order OR
                  source_work.vendor_identity_id<>vendor OR source_revision<=source_work.order_revision) THEN
        RAISE EXCEPTION 'order work cancellation does not follow placement' USING ERRCODE='PX101';
    END IF;
    result_work_id := source_work.work_item_id;
    result_outcome := 'NO_WORK';
    IF result_work_id IS NOT NULL THEN
        SELECT * INTO current_work FROM supply.work_items WHERE id=result_work_id FOR UPDATE;
        IF NOT FOUND OR current_work.tenant_id<>tenant OR current_work.country<>home_country OR
           current_work.vendor_identity_id<>vendor OR
           current_work.reference_type<>'MARKETPLACE_ORDER' OR current_work.reference_id<>source_order THEN
            RAISE EXCEPTION 'order work scope changed' USING ERRCODE='PX101';
        END IF;
        CASE current_work.status
            WHEN 'CANCELLED' THEN result_outcome := 'ALREADY_CANCELLED';
            WHEN 'COMPLETED' THEN result_outcome := 'ALREADY_COMPLETED';
            WHEN 'REJECTED' THEN result_outcome := 'ALREADY_REJECTED';
            ELSE
                UPDATE supply.work_items SET status='CANCELLED',updated_at=occurred_at
                WHERE id=result_work_id;
                result_outcome := 'CANCELLED';
        END CASE;
    END IF;
    INSERT INTO supply.order_work_cancellations
        (source_cancellation_id,source_work_intent_id,tenant_id,country,
         customer_identity_id,order_id,vendor_identity_id,order_revision,
         order_status,payload_fingerprint,source_payload,outcome,work_item_id,created_at)
    VALUES (source_cancel_id,source_work_id,tenant,home_country,customer,
            source_order,vendor,source_revision,source_status,source_fingerprint,
            payload,result_outcome,result_work_id,occurred_at);
    RETURN QUERY SELECT result_work_id,result_outcome,false;
END $$;
REVOKE ALL ON FUNCTION supply.ingest_order_work_cancellation(uuid,uuid,uuid,char(2),uuid,uuid,uuid,bigint,text,char(64),jsonb,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION supply.ingest_order_work_cancellation(uuid,uuid,uuid,char(2),uuid,uuid,uuid,bigint,text,char(64),jsonb,timestamptz) TO planext4u_transaction_runtime;
RESET ROLE;
