// Exercises contact-route fail-closed behavior on the persistent synthetic
// local stack. No provider tokens, session tokens or phone numbers enter output.
import {randomUUID} from 'node:crypto';
import {writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {stackDirectory} from './stack.mjs';

const base = 'http://127.0.0.1:8082';
const checks = [];
async function request(path, method = 'GET', token = '', body) {
  const response = await fetch(base + path, {
    method,
    headers: {
      'X-Correlation-ID': randomUUID(),
      ...(token ? {Authorization: `Bearer ${token}`} : {}),
      ...(body === undefined ? {} : {'Content-Type': 'application/json'}),
    },
    ...(body === undefined ? {} : {body: JSON.stringify(body)}),
    signal: AbortSignal.timeout(10000),
    redirect: 'error',
  });
  return {status: response.status, body: await response.json()};
}
function check(id, actual, expected, predicate = () => true) {
  const result = actual.status === expected && predicate(actual.body) ? 'PASSED' : 'FAILED';
  checks.push({id, result, status: actual.status});
  if (result !== 'PASSED') throw new Error(`${id} failed`);
}
let failure;
try {
  const path = '/v1/me/contact-matching';
  check('anonymous-contacts-denied', await request(path), 401);
  const auth = await request('/v1/auth/exchange', 'POST', '', {
    provider: 'local', provider_token: `synthetic-participant-${randomUUID()}`,
    device_id: `contact-smoke-${randomUUID()}`, country: 'IN',
  });
  check('fresh-local-customer', auth, 201, body => body.tokens?.access_token?.length > 20);
  const token = auth.body.tokens.access_token;
  check('legacy-export-stable', await request('/v1/me/data-export', 'GET', token), 200,
    body => body.schema_version === 1 && !('contact_snapshots' in body));
  check('versioned-export-safe-metadata', await request('/v1/me/data-export?schema=2', 'GET', token), 200,
    body => body.schema_version === 2 && Array.isArray(body.contact_snapshots) &&
      body.contact_snapshots.length === 0 &&
      body.coverage?.included?.includes('identity.contact_snapshot_metadata') &&
      !JSON.stringify(body).includes('phone_hash') && !JSON.stringify(body).includes('key_id'));
  check('versioned-export-safe-deletion-history', await request('/v1/me/data-export?schema=3', 'GET', token), 200,
    body => body.schema_version === 3 && Array.isArray(body.contact_snapshots) &&
      Array.isArray(body.deletion_history) && body.deletion_history.length === 0 &&
      body.coverage?.included?.includes('identity.deletion_history') &&
      !body.coverage?.excluded?.includes('deletion_history'));
  check('unknown-export-version-denied', await request('/v1/me/data-export?schema=4', 'GET', token), 422);
  check('ambiguous-export-version-denied', await request('/v1/me/data-export?schema=2&schema=2', 'GET', token), 422);
  check('synthetic-provider-matching-unavailable', await request(path, 'GET', token), 200,
    body => body.status === 'UNAVAILABLE' && !('items' in body));
  check('client-fields-rejected', await request(path, 'PUT', token, {
    provider_token: 'synthetic-proof', policy_version: 'v1', expected_revision: 0,
    phones: ['+919999999999'], identity_id: 'another-account',
  }), 422);
  check('unconfigured-provider-sync-denied', await request(path, 'PUT', token, {
    provider_token: 'synthetic-proof', policy_version: 'v1', expected_revision: 0,
    phones: ['+919999999999'],
  }), 503);
  check('unconfigured-provider-directory-denied',
    await request(`${path}/matches?revision=1`, 'GET', token), 503);
  check('synthetic-provider-remains-unavailable', await request(path, 'GET', token), 200,
    body => body.status === 'UNAVAILABLE');
} catch (error) { failure = error.message; }
const report = {
  check_id: 'contact-matching-local-synthetic', checked_at: new Date().toISOString(),
  result: failure ? 'FAILED' : 'PASSED', checks,
  ...(failure ? {failure} : {}),
  note: 'Actual persistent local gateway/identity requests. This synthetic provider cannot enable Firebase contact matching; real provider and device evidence remains pending.',
};
writeFileSync(resolve(stackDirectory, 'contact-smoke.json'), `${JSON.stringify(report, null, 2)}\n`);
console.log(JSON.stringify(report, null, 2));
process.exitCode = failure ? 2 : 0;
