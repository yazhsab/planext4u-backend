\set ON_ERROR_STOP on

-- Local readiness fixture only. These zero-fee values are not launch finance
-- rules. Preserve any existing published policy; never reset financial data.
DO $phase0$
BEGIN
    IF current_database() <> 'planext4u_local' THEN
        RAISE EXCEPTION 'Phase 0 reference fixture requires the isolated local database';
    END IF;
END
$phase0$;

-- Explicitly provision only the two known synthetic provider subjects. Normal
-- sign-in correctly grants CUSTOMER only and must not trust provider role claims.
INSERT INTO identity.identities (id,tenant_id,provider,provider_subject,created_at)
VALUES
    ('44000000-0000-4000-8000-000000000002','afc1e0db-73cf-40b3-9927-33590133da0b','local','vendor-synthetic-001',now()),
    ('44000000-0000-4000-8000-000000000003','afc1e0db-73cf-40b3-9927-33590133da0b','local','rider-synthetic-001',now())
ON CONFLICT DO NOTHING;

INSERT INTO identity.profiles (identity_id,display_name,locale,time_zone,version,updated_at)
SELECT id,'Synthetic local role fixture','en','Asia/Kolkata',1,now()
FROM identity.identities
WHERE tenant_id='afc1e0db-73cf-40b3-9927-33590133da0b' AND provider='local'
  AND provider_subject IN ('vendor-synthetic-001','rider-synthetic-001')
ON CONFLICT DO NOTHING;

INSERT INTO identity.identity_roles (identity_id,role,granted_at)
SELECT identity.id,fixture.role,now()
FROM identity.identities AS identity
JOIN (VALUES ('vendor-synthetic-001','VENDOR'),('rider-synthetic-001','RIDER'))
    AS fixture(subject,role) ON fixture.subject=identity.provider_subject
WHERE identity.tenant_id='afc1e0db-73cf-40b3-9927-33590133da0b' AND identity.provider='local'
ON CONFLICT DO NOTHING;

INSERT INTO commerce.pricing_policies
    (id,tenant_id,country,version,product_tax_basis_points,product_tax_treatment,
     platform_fee_minor,platform_fee_tax_basis_points,wallet_point_value_minor,
     wallet_mode,quote_ttl_seconds,reservation_ttl_seconds,active,published_at,published_by)
SELECT
    '9d7a8088-b66c-4e51-b166-c1c6511f8979',
    'afc1e0db-73cf-40b3-9927-33590133da0b','IN','phase0-synthetic-zero-fees-v1',
    0,'INCLUSIVE',0,0,100,'HYBRID_PAYMENT',600,600,true,now(),
    '1905509c-b808-4515-8a1d-d5e617bba8bf'
WHERE NOT EXISTS (
    SELECT 1 FROM commerce.pricing_policies
    WHERE tenant_id='afc1e0db-73cf-40b3-9927-33590133da0b' AND country='IN' AND active
)
ON CONFLICT DO NOTHING;
