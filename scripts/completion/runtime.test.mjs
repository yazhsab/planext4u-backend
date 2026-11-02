import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, readFileSync, readdirSync, unlinkSync, rmdirSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {createPrivateKey, createPublicKey} from 'node:crypto';
import {prepareLocal, loadLocalProfile, services} from './runtime.mjs';

function temporary(run) {
  const directory = mkdtempSync(join(tmpdir(), 'planext4u-runtime-test-'));
  try { run(directory); }
  finally { for (const file of readdirSync(directory)) unlinkSync(join(directory, file)); rmdirSync(directory); }
}
test('preparation is resumable and keeps matching JWT keys and secrets', () => temporary(directory => {
  prepareLocal(directory);
  const first = Object.fromEntries(readdirSync(directory).map(file => [file, readFileSync(join(directory, file), 'utf8')]));
  prepareLocal(directory);
  for (const [file, content] of Object.entries(first)) assert.equal(readFileSync(join(directory, file), 'utf8'), content);
  const privateKey = createPrivateKey(first['jwt-private.pem']);
  assert.equal(createPublicKey(privateKey).export({type: 'spki', format: 'pem'}), first['jwt-public.pem']);
  for (const name of Object.keys(first).filter(value => value.endsWith('.key'))) assert.equal(Buffer.byteLength(first[name].trim()), 32);
}));
test('all service profiles use loopback development and the same tenant/key contract', () => temporary(directory => {
  prepareLocal(directory);
  const profiles = services.map(service => loadLocalProfile(service, directory));
  for (const profile of profiles) { assert.equal(profile.APP_ENV, 'development'); assert.match(profile.HTTP_ADDRESS, /^127\.0\.0\.1:/); }
  assert.equal(profiles[0].TENANT_ID, profiles[3].TENANT_ID);
  assert.equal(profiles[0].JWT_AUDIENCE, profiles[5].JWT_AUDIENCE);
  assert.equal(profiles[0].JWT_KEY_ID, profiles[5].JWT_KEY_ID);
  assert.equal(profiles[4].ADMIN_REQUIRE_MFA, 'true');
  assert.ok(profiles[4].ADMIN_ALLOWED_ORIGINS.split(',').every(origin => origin.startsWith('https://')));
  const routes = JSON.parse(profiles[5].ANONYMOUS_ROUTES);
  const identity = JSON.parse(readFileSync(new URL('../../api/openapi/identity.openapi.json', import.meta.url), 'utf8'));
  const configuration = JSON.parse(readFileSync(new URL('../../api/openapi/configuration.openapi.json', import.meta.url), 'utf8'));
  assert.equal(profiles[1].PUBLIC_STARTUP_TENANT_ID,profiles[0].TENANT_ID);
  assert.equal(profiles[1].PUBLIC_STARTUP_COUNTRIES,profiles[0].ALLOWED_COUNTRIES);
  assert.deepEqual(routes['/v1/startup'],['GET']);
  for (const [route,methods] of Object.entries(routes)) {
    const owner=route==='/v1/startup'?configuration:identity;
    assert.deepEqual(methods,route==='/v1/startup'?['GET']:['POST']);
    for(const method of methods)assert.ok(owner.paths[route]?.[method.toLowerCase()],`${route} ${method} must match its owning API contract`);
  }
}));
test('local launcher rejects production and public bind profiles', () => temporary(directory => {
  prepareLocal(directory);
  const profile = loadLocalProfile('admin', directory);
  for (const change of [{APP_ENV: 'production'}, {HTTP_ADDRESS: '0.0.0.0:8090'}]) {
    writeFileSync(join(directory, 'admin.json'), JSON.stringify({...profile, ...change}));
    assert.throws(() => loadLocalProfile('admin', directory), /development mode and a loopback/);
  }
  assert.throws(() => loadLocalProfile('../admin', directory), /Unknown/);
  writeFileSync(join(directory, 'migration.database.url'), 'postgres://test:test@remote.example:5432/production');
  assert.throws(() => loadLocalProfile('migration', directory), /isolated loopback database/);
}));
test('an incomplete key pair cannot silently rotate existing sessions', () => temporary(directory => {
  prepareLocal(directory);
  unlinkSync(join(directory, 'jwt-public.pem'));
  assert.throws(() => prepareLocal(directory), /Incomplete local JWT/);
}));
