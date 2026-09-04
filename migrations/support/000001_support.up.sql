CREATE SCHEMA IF NOT EXISTS support AUTHORIZATION planext4u_support_owner;
ALTER SCHEMA support OWNER TO planext4u_support_owner;
REVOKE ALL ON SCHEMA support FROM PUBLIC;
SET ROLE planext4u_support_owner;

CREATE TABLE support.tickets (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    owner_identity_id text NOT NULL,
    owner_role text NOT NULL CHECK (owner_role IN ('CUSTOMER', 'VENDOR', 'RIDER')),
    category text NOT NULL CHECK (category IN ('ACCOUNT', 'ORDER', 'PAYMENT', 'VENDOR_OPERATIONS', 'RIDER_OPERATIONS', 'OTHER')),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 4 AND 160),
    related_reference text,
    priority text NOT NULL CHECK (priority IN ('LOW', 'NORMAL', 'HIGH', 'URGENT')),
    status text NOT NULL CHECK (status IN ('OPEN', 'WAITING_FOR_SUPPORT', 'WAITING_FOR_REQUESTER', 'RESOLVED', 'CLOSED')),
    create_idempotency_key text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, country, owner_identity_id, owner_role, create_idempotency_key)
);
CREATE INDEX support_ticket_owner_list_idx
    ON support.tickets (tenant_id, country, owner_identity_id, owner_role, created_at DESC, id DESC);
CREATE INDEX support_ticket_admin_queue_idx
    ON support.tickets (tenant_id, country, status, priority, created_at, id);

CREATE TABLE support.messages (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    ticket_id uuid NOT NULL REFERENCES support.tickets(id) ON DELETE CASCADE,
    author_type text NOT NULL CHECK (author_type IN ('REQUESTER', 'SUPPORT')),
    author_identity_id text NOT NULL,
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 8000),
    idempotency_key text,
    request_fingerprint char(64) NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (ticket_id, idempotency_key)
);
CREATE INDEX support_message_timeline_idx ON support.messages (ticket_id, created_at, id);

GRANT USAGE ON SCHEMA support TO planext4u_support_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA support TO planext4u_support_runtime;
RESET ROLE;
