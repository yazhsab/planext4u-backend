SET ROLE planext4u_catalog_owner;

CREATE TABLE catalog.customer_questions (
    id text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    item_id uuid NOT NULL REFERENCES catalog.items(id),
    customer_identity_id text NOT NULL CHECK (length(customer_identity_id) BETWEEN 1 AND 128),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    question_text text NOT NULL CHECK (char_length(question_text) BETWEEN 5 AND 500),
    status text NOT NULL CHECK (status IN ('PENDING', 'ANSWERED', 'HIDDEN')),
    answer_text text CHECK (answer_text IS NULL OR char_length(answer_text) BETWEEN 1 AND 4000),
    answered_by text,
    asked_at timestamptz NOT NULL,
    answered_at timestamptz,
    CONSTRAINT catalog_question_idempotency_unique UNIQUE (tenant_id, customer_identity_id, idempotency_key),
    CONSTRAINT catalog_question_answer_consistent CHECK (
        (status = 'ANSWERED' AND answer_text IS NOT NULL AND answered_by IS NOT NULL AND answered_at IS NOT NULL)
        OR (status <> 'ANSWERED' AND answer_text IS NULL AND answered_by IS NULL AND answered_at IS NULL)
    )
);
CREATE INDEX catalog_questions_item_status_idx
    ON catalog.customer_questions (tenant_id, country, item_id, status, asked_at DESC);
CREATE INDEX catalog_questions_customer_pending_idx
    ON catalog.customer_questions (tenant_id, customer_identity_id, item_id, asked_at DESC)
    WHERE status = 'PENDING';

GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.customer_questions TO planext4u_catalog_runtime;
RESET ROLE;
