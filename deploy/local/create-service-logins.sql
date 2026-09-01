\getenv identity_password PLANEXT4U_LOCAL_IDENTITY_PASSWORD
\getenv configuration_password PLANEXT4U_LOCAL_CONFIGURATION_PASSWORD
\getenv catalog_password PLANEXT4U_LOCAL_CATALOG_PASSWORD
\getenv media_password PLANEXT4U_LOCAL_MEDIA_PASSWORD
\getenv audit_password PLANEXT4U_LOCAL_AUDIT_PASSWORD
\getenv messaging_password PLANEXT4U_LOCAL_MESSAGING_PASSWORD
\getenv notification_password PLANEXT4U_LOCAL_NOTIFICATION_PASSWORD
\getenv transaction_password PLANEXT4U_LOCAL_TRANSACTION_PASSWORD
\getenv admin_password PLANEXT4U_LOCAL_ADMIN_PASSWORD

SELECT format('CREATE ROLE planext4u_identity_login LOGIN PASSWORD %L', :'identity_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_identity_login') \gexec
SELECT format('ALTER ROLE planext4u_identity_login PASSWORD %L', :'identity_password') \gexec
GRANT planext4u_identity_runtime TO planext4u_identity_login;

SELECT format('CREATE ROLE planext4u_configuration_login LOGIN PASSWORD %L', :'configuration_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_configuration_login') \gexec
SELECT format('ALTER ROLE planext4u_configuration_login PASSWORD %L', :'configuration_password') \gexec
GRANT planext4u_configuration_runtime TO planext4u_configuration_login;

SELECT format('CREATE ROLE planext4u_catalog_login LOGIN PASSWORD %L', :'catalog_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_catalog_login') \gexec
SELECT format('ALTER ROLE planext4u_catalog_login PASSWORD %L', :'catalog_password') \gexec
GRANT planext4u_catalog_runtime TO planext4u_catalog_login;

SELECT format('CREATE ROLE planext4u_media_login LOGIN PASSWORD %L', :'media_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_media_login') \gexec
SELECT format('ALTER ROLE planext4u_media_login PASSWORD %L', :'media_password') \gexec
GRANT planext4u_media_runtime TO planext4u_media_login;

SELECT format('CREATE ROLE planext4u_audit_login LOGIN PASSWORD %L', :'audit_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_audit_login') \gexec
SELECT format('ALTER ROLE planext4u_audit_login PASSWORD %L', :'audit_password') \gexec
GRANT planext4u_audit_runtime TO planext4u_audit_login;

SELECT format('CREATE ROLE planext4u_messaging_login LOGIN PASSWORD %L', :'messaging_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_messaging_login') \gexec
SELECT format('ALTER ROLE planext4u_messaging_login PASSWORD %L', :'messaging_password') \gexec
GRANT planext4u_messaging_runtime TO planext4u_messaging_login;

SELECT format('CREATE ROLE planext4u_notification_login LOGIN PASSWORD %L', :'notification_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_notification_login') \gexec
SELECT format('ALTER ROLE planext4u_notification_login PASSWORD %L', :'notification_password') \gexec
GRANT planext4u_notification_runtime TO planext4u_notification_login;

SELECT format('CREATE ROLE planext4u_transaction_login LOGIN PASSWORD %L', :'transaction_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_transaction_login') \gexec
SELECT format('ALTER ROLE planext4u_transaction_login PASSWORD %L', :'transaction_password') \gexec
GRANT planext4u_transaction_runtime TO planext4u_transaction_login;

SELECT format('CREATE ROLE planext4u_admin_login LOGIN PASSWORD %L', :'admin_password') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'planext4u_admin_login') \gexec
SELECT format('ALTER ROLE planext4u_admin_login PASSWORD %L', :'admin_password') \gexec
GRANT planext4u_admin_runtime, planext4u_configuration_runtime, planext4u_audit_runtime TO planext4u_admin_login;
