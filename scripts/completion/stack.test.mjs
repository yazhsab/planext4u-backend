import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, readdirSync, readFileSync, writeFileSync, unlinkSync, rmdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {prepareLocal, services} from './runtime.mjs';
import {createStackDefinition, ports} from './stack.mjs';

function temporary(run) {
  const directory = mkdtempSync(join(tmpdir(), 'planext4u-stack-test-'));
  try { prepareLocal(directory); run(directory); }
  finally { for (const file of readdirSync(directory)) unlinkSync(join(directory, file)); rmdirSync(directory); }
}
test('container startup preserves shared keys while moving service connections onto the internal network', () => temporary(directory => {
  const before = readFileSync(join(directory, 'jwt-private.pem'), 'utf8');
  const stack = createStackDefinition(directory).services;
  assert.equal(stack.identity.environment.JWT_PRIVATE_KEY_FILE_VALUE, before);
  assert.equal(readFileSync(join(directory, 'jwt-private.pem'), 'utf8'), before);
  assert.equal(stack.gateway.environment.JWT_PUBLIC_KEY_FILE_VALUE, readFileSync(join(directory, 'jwt-public.pem'), 'utf8'));
  assert.equal(stack.identity.environment.PROVIDER_BASE_URL, 'http://synthetic-provider:8081');
  assert.equal(stack.gateway.environment.REDIS_URL_FILE_VALUE, 'redis://redis:6379/0');
  for (const service of services.filter(name => name !== 'gateway')) {
    const url = new URL(stack[service].environment.DATABASE_URL_FILE_VALUE);
    assert.equal(url.hostname, 'postgres');
    assert.equal(url.port, '5432');
    assert.equal(url.username, `planext4u_${service}_login`);
    assert.equal(stack.postgres.environment[`PLANEXT4U_LOCAL_${service.toUpperCase()}_PASSWORD`], url.password);
  }
  assert.deepEqual(stack.migration.command, ['/app/migrate','-root','/app/migrations','-service','all']);
  assert.equal(new URL(stack.migration.environment.MIGRATION_DATABASE_URL_FILE_VALUE).username, 'planext4u_local');
}));
test('every API has a loopback host port and real readiness check; gateway routes target the owners', () => temporary(directory => {
  const stack = createStackDefinition(directory).services;
  for (const service of services) {
    assert.deepEqual(stack[service].ports, [`127.0.0.1:${ports[service]}:${ports[service]}`]);
    assert.equal(stack[service].environment.APP_ENV, 'development');
    assert.equal(stack[service].environment.HTTP_ADDRESS, `0.0.0.0:${ports[service]}`);
    assert.ok(stack[service].healthcheck.test.at(-1).endsWith('/readyz'));
    assert.equal(stack[service].depends_on.redis.condition, 'service_healthy');
  }
  const routes = JSON.parse(stack.gateway.environment.UPSTREAM_ROUTES);
  assert.equal(routes['/v1/auth'], 'http://identity:8083');
  assert.equal(routes['/v1/bootstrap'], 'http://configuration:8084');
  assert.equal(routes['/v1/pages'], 'http://configuration:8084');
  const hostRoutes = JSON.parse(JSON.parse(readFileSync(join(directory, 'gateway.json'), 'utf8')).UPSTREAM_ROUTES);
  assert.equal(hostRoutes['/v1/pages'], 'http://127.0.0.1:8084');
  assert.equal(routes['/v1/home'], 'http://catalog:8085');
  assert.equal(routes['/v1/media'], 'http://media:8087');
  assert.equal(stack.transaction.environment.PRIVATE_MEDIA_BASE_URL, 'http://media:8087');
  assert.equal(stack.media.environment.S3_ENDPOINT, 'http://object-store:9000');
  assert.equal(stack.media.environment.S3_PUBLIC_ENDPOINT, 'http://127.0.0.1:9000');
  assert.deepEqual(stack.media.volumes, ['clamav-socket:/run/clamav:ro']);
  assert.equal(stack.media.environment.PLANEXT4U_SUPPLEMENTARY_GROUPS, '101');
  for (const service of services.filter(name => name !== 'media')) assert.equal(stack[service].environment.PLANEXT4U_SUPPLEMENTARY_GROUPS, undefined);
  assert.equal(stack.admin.environment.ADMIN_REQUIRE_MFA, 'true');
  assert.ok(stack.admin.environment.ADMIN_ALLOWED_ORIGINS.startsWith('https://'));
}));
test('local container generation rejects remote databases and file references outside the local runtime', () => temporary(directory => {
  writeFileSync(join(directory, 'identity.database.url'), 'postgres://test:test@remote.example:5432/production');
  assert.throws(() => createStackDefinition(directory), /isolated loopback database/);
  writeFileSync(join(directory, 'identity.database.url'), 'postgres://planext4u_identity_login:test@127.0.0.1:54320/planext4u_local');
  const profile = JSON.parse(readFileSync(join(directory, 'identity.json'), 'utf8'));
  profile.JWT_PRIVATE_KEY_FILE = join(directory, '../unrelated-private-key.pem');
  writeFileSync(join(directory, 'identity.json'), JSON.stringify(profile));
  assert.throws(() => createStackDefinition(directory), /isolated local runtime/);
}));
test('administrator database credentials cannot be substituted for a restricted service account', () => temporary(directory => {
  writeFileSync(join(directory, 'identity.database.url'), 'postgres://planext4u_local:test@127.0.0.1:54320/planext4u_local');
  assert.throws(() => createStackDefinition(directory), /restricted service login/);
}));
test('Compose interpolation cannot change a locally configured password and providers stay inactive', () => temporary(directory => {
  writeFileSync(join(directory, 'identity.database.url'), 'postgres://planext4u_identity_login:local$literal@127.0.0.1:54320/planext4u_local');
  const stack = createStackDefinition(directory).services;
  assert.equal(stack.postgres.environment.PLANEXT4U_LOCAL_IDENTITY_PASSWORD, 'local$$literal');
  assert.ok(stack.identity.environment.DATABASE_URL_FILE_VALUE.includes('local$$literal'));
  for (const name of ['RAZORPAY_KEY_ID','RAZORPAY_API_SECRET_FILE','PAYSTACK_PUBLIC_KEY','PAYSTACK_API_SECRET_FILE','NOTIFICATION_BASE_URL','BANK_VERIFICATION_BASE_URL','BANK_VERIFICATION_TOKEN_FILE','KYC_VERIFICATION_BASE_URL','KYC_VERIFICATION_TOKEN_FILE','OCR_VERIFICATION_BASE_URL','OCR_VERIFICATION_TOKEN_FILE']) {
    assert.equal(stack.transaction.environment[name], '');
    assert.equal(stack.transaction.environment[`${name}_VALUE`], undefined);
  }
}));

test('explicit local OCR switch uses only the synthetic provider and never inherits an external endpoint', () => temporary(directory => {
  const file = join(directory, 'transaction.json');
  const profile = JSON.parse(readFileSync(file, 'utf8'));
  writeFileSync(file, JSON.stringify({...profile, OCR_VERIFICATION_BASE_URL: 'https://external.example.test', OCR_VERIFICATION_TOKEN_FILE: join(directory, 'jwt-private.pem')}));
  const inactive = createStackDefinition(directory).services.transaction.environment;
  assert.equal(inactive.OCR_VERIFICATION_BASE_URL, '');
  assert.equal(inactive.OCR_VERIFICATION_TOKEN_FILE, '');
  assert.equal(inactive.OCR_VERIFICATION_TOKEN_FILE_VALUE, undefined);
  writeFileSync(file, JSON.stringify({...profile, LOCAL_SYNTHETIC_OCR: 'true', OCR_VERIFICATION_BASE_URL: 'https://external.example.test', OCR_VERIFICATION_TOKEN_FILE: join(directory, 'jwt-private.pem')}));
  const active = createStackDefinition(directory).services.transaction.environment;
  assert.equal(active.OCR_VERIFICATION_BASE_URL, 'http://synthetic-provider:8081');
  assert.equal(active.OCR_VERIFICATION_TOKEN_FILE, '/run/planext4u-secrets/ocr-resolver-token');
  assert.equal(active.OCR_VERIFICATION_TOKEN_FILE_VALUE, 'synthetic-local-ocr-provider-only-token');
}));

test('explicit local KYC switch uses only the synthetic provider and never inherits an external endpoint', () => temporary(directory => {
  const file = join(directory, 'transaction.json');
  const profile = JSON.parse(readFileSync(file, 'utf8'));
  writeFileSync(file, JSON.stringify({...profile, KYC_VERIFICATION_BASE_URL: 'https://external.example.test', KYC_VERIFICATION_TOKEN_FILE: join(directory, 'jwt-private.pem')}));
  const inactive = createStackDefinition(directory).services.transaction.environment;
  assert.equal(inactive.KYC_VERIFICATION_BASE_URL, '');
  assert.equal(inactive.KYC_VERIFICATION_TOKEN_FILE, '');
  assert.equal(inactive.KYC_VERIFICATION_TOKEN_FILE_VALUE, undefined);
  writeFileSync(file, JSON.stringify({...profile, LOCAL_SYNTHETIC_KYC: 'true', KYC_VERIFICATION_BASE_URL: 'https://external.example.test', KYC_VERIFICATION_TOKEN_FILE: join(directory, 'jwt-private.pem')}));
  const active = createStackDefinition(directory).services.transaction.environment;
  assert.equal(active.KYC_VERIFICATION_BASE_URL, 'http://synthetic-provider:8081');
  assert.equal(active.KYC_VERIFICATION_TOKEN_FILE, '/run/planext4u-secrets/kyc-resolver-token');
  assert.equal(active.KYC_VERIFICATION_TOKEN_FILE_VALUE, 'synthetic-local-kyc-provider-only-token');
}));

test('explicit local bank switch selects only the synthetic hosted provider and independent resolver', () => temporary(directory => {
  const file = join(directory, 'transaction.json');
  const profile = JSON.parse(readFileSync(file, 'utf8'));
  writeFileSync(file, JSON.stringify({...profile, LOCAL_SYNTHETIC_BANK_LINKS: 'true', BANK_LINK_BASE_URL: 'https://external.example.test'}));
  const transaction = createStackDefinition(directory).services.transaction.environment;
  assert.equal(transaction.BANK_LINK_BASE_URL, 'http://synthetic-provider:8081');
  assert.equal(transaction.BANK_VERIFICATION_BASE_URL, 'http://synthetic-provider:8081');
  assert.deepEqual(JSON.parse(transaction.BANK_LINK_LAUNCH_ORIGINS), ['http://127.0.0.1:18081']);
  assert.equal(transaction.BANK_LINK_TOKEN_FILE_VALUE, 'synthetic-local-bank-provider-only-token');
  assert.equal(transaction.RAZORPAY_KEY_ID, '');
}));
