SET ROLE planext4u_configuration_owner;

UPDATE configuration.snapshots
SET document = jsonb_set(
    jsonb_set(document, '{minimum_versions}', (document -> 'minimum_versions') - 'WEB'),
    '{latest_versions}', (document -> 'latest_versions') - 'WEB'
);

UPDATE configuration.workspace_drafts
SET document = jsonb_set(
    jsonb_set(document, '{minimum_versions}', (document -> 'minimum_versions') - 'WEB'),
    '{latest_versions}', (document -> 'latest_versions') - 'WEB'
);

RESET ROLE;
