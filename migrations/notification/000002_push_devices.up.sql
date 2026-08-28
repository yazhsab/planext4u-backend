CREATE TABLE notification.push_device (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  country char(2) NOT NULL CHECK (country = upper(country)),
  subject_id text NOT NULL,
  device_reference text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('ANDROID', 'IOS')),
  locale text NOT NULL,
  token_ciphertext bytea NOT NULL,
  token_key_version integer NOT NULL CHECK (token_key_version > 0),
  enabled boolean NOT NULL DEFAULT true,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, subject_id, device_reference)
);

CREATE INDEX push_device_delivery_lookup
  ON notification.push_device (tenant_id, country, subject_id)
  WHERE enabled;
