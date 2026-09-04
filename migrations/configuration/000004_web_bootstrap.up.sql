SET ROLE planext4u_configuration_owner;

UPDATE configuration.snapshots
SET document = jsonb_set(
    jsonb_set(document, '{minimum_versions,WEB}', to_jsonb(COALESCE(document #>> '{minimum_versions,IOS}', '0.0.0')), true),
    '{latest_versions,WEB}', to_jsonb(COALESCE(document #>> '{latest_versions,IOS}', '0.0.0')), true
)
WHERE NOT (document -> 'minimum_versions' ? 'WEB')
   OR NOT (document -> 'latest_versions' ? 'WEB');

UPDATE configuration.workspace_drafts
SET document = jsonb_set(
    jsonb_set(document, '{minimum_versions,WEB}', to_jsonb(COALESCE(document #>> '{minimum_versions,IOS}', '0.0.0')), true),
    '{latest_versions,WEB}', to_jsonb(COALESCE(document #>> '{latest_versions,IOS}', '0.0.0')), true
)
WHERE NOT (document -> 'minimum_versions' ? 'WEB')
   OR NOT (document -> 'latest_versions' ? 'WEB');

RESET ROLE;
