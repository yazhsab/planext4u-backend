CREATE SCHEMA IF NOT EXISTS governance AUTHORIZATION planext4u_governance_owner;
ALTER SCHEMA governance OWNER TO planext4u_governance_owner;
REVOKE ALL ON SCHEMA governance FROM PUBLIC;
SET ROLE planext4u_governance_owner;

CREATE TABLE governance.country_controls (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    currency char(3) NOT NULL,
    locales text[] NOT NULL,
    feature_flags jsonb NOT NULL,
    policy_version text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    published_at timestamptz NOT NULL,
    published_by_identity_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, country),
    CHECK (jsonb_typeof(feature_flags)='object')
);

CREATE TABLE governance.report_cards (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    id text NOT NULL,
    title text NOT NULL,
    domain text NOT NULL,
    metric text NOT NULL,
    value bigint NOT NULL,
    unit text NOT NULL,
    freshness timestamptz NOT NULL,
    published boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, country, id),
    FOREIGN KEY (tenant_id,country) REFERENCES governance.country_controls(tenant_id,country) ON DELETE CASCADE
);

CREATE TABLE governance.map_cells (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    region_code text NOT NULL,
    label text NOT NULL,
    count bigint NOT NULL CHECK (count >= 0),
    intensity smallint NOT NULL CHECK (intensity BETWEEN 0 AND 100),
    published boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, country, region_code),
    FOREIGN KEY (tenant_id,country) REFERENCES governance.country_controls(tenant_id,country) ON DELETE CASCADE
);

CREATE TABLE governance.leaderboard_entries (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    board text NOT NULL,
    rank integer NOT NULL CHECK (rank > 0),
    masked_label text NOT NULL,
    score bigint NOT NULL,
    badge text NOT NULL,
    published boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, country, board, rank),
    FOREIGN KEY (tenant_id,country) REFERENCES governance.country_controls(tenant_id,country) ON DELETE CASCADE
);

CREATE TABLE governance.insights (
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    id text NOT NULL,
    title text NOT NULL,
    summary text NOT NULL,
    confidence text NOT NULL CHECK (confidence IN ('LOW','MEDIUM','HIGH')),
    evidence text[] NOT NULL,
    generated_at timestamptz NOT NULL,
    published boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, country, id),
    FOREIGN KEY (tenant_id,country) REFERENCES governance.country_controls(tenant_id,country) ON DELETE CASCADE
);

CREATE TABLE governance.publication_audit (
    sequence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL,
    country char(2) NOT NULL,
    policy_version text NOT NULL,
    revision bigint NOT NULL,
    published_by_identity_id uuid NOT NULL,
    correlation_id text NOT NULL,
    published_at timestamptz NOT NULL
);

GRANT USAGE ON SCHEMA governance TO planext4u_governance_runtime, planext4u_governance_publisher;
GRANT SELECT ON ALL TABLES IN SCHEMA governance TO planext4u_governance_runtime, planext4u_governance_publisher;
GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA governance TO planext4u_governance_publisher;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA governance TO planext4u_governance_publisher;

RESET ROLE;
