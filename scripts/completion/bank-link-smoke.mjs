import {randomUUID, createHash} from 'node:crypto';
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';

const output = resolve(repositoryRoot, '.local/completion/bank-link-runtime');
mkdirSync(output, {recursive: true});
const gateway = 'http://127.0.0.1:8082';
const checks = [], sessions = [];
const runID = randomUUID();
const runFile = resolve(output, `${runID}.json`);
const inventory = () => {
  const listed = spawnSync('git', ['ls-files','-c','-o','--exclude-standard','-z','--','internal/bankverification','internal/fakeprovider','cmd/transaction','migrations','scripts/completion/bank-link-smoke.mjs','scripts/completion/stack.mjs'], {cwd: repositoryRoot, encoding: 'utf8', windowsHide: true});
  if (listed.status !== 0) throw new Error('Cannot identify current bank-link sources.');
  return [...new Set(listed.stdout.split('\0').filter(Boolean))].sort().map(path => ({path, sha256:createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}));
};
const before = inventory();
async function request(url, {method='GET', token, body, headers={}}={}) {
  const response = await fetch(url, {method,headers:{...(method!=='GET'?{'Idempotency-Key':randomUUID()}:{}),...(token ? {Authorization:`Bearer ${token}`} : {}),...(body !== undefined ? {'Content-Type':'application/json'} : {}),...headers},...(body !== undefined ? {body:JSON.stringify(body)} : {}),redirect:'error',signal:AbortSignal.timeout(12000)});
  const text = await response.text();
  let data; try {data=JSON.parse(text);} catch {data=null;}
  return {status:response.status, headers:response.headers, data, text};
}
function check(id, response, expected, predicate=()=>true) {
  const passed = response.status===expected && predicate(response);
  checks.push({id,status:response.status,result:passed?'PASSED':'FAILED',...(response.data?.error?.code?{error_code:response.data.error.code}:{})});
  if (!passed) throw new Error(`${id}: HTTP ${response.status}, expected ${expected}`);
  return response.data;
}
async function enroll(role) {
  const data = check(`${role}-fresh-enrollment`,await request(`${gateway}/v1/auth/enroll`,{method:'POST',body:{provider:'local',provider_token:`synthetic-participant-${randomUUID()}`,device_id:`bank-link-${runID}-${role}`,country:'IN',enroll_as:role}}),201,r=>r.data.roles.includes(role));
  sessions.push(data.tokens);
  return data;
}
let failure;
try {
  check('anonymous-bank-link-denied',await request(`${gateway}/v1/vendor/bank-links`,{method:'POST',body:{}}),401);
  const other = await enroll('VENDOR');
  for (const role of ['VENDOR','RIDER']) {
    const identity = await enroll(role), token = identity.tokens.access_token;
    const prefix = `${gateway}/v1/${role.toLowerCase()}/bank-links`;
    check(`${role}-no-current-attempt`,await request(`${prefix}/current`,{token}),404,r=>r.data.error.code==='BANK_LINK_NOT_FOUND');
    check(`${role}-client-raw-bank-fields-rejected`,await request(prefix,{method:'POST',token,headers:{'Idempotency-Key':randomUUID()},body:{account_number:'synthetic-prohibited-field'}}),400);
    const start = check(`${role}-normal-owner-start`,await request(prefix,{method:'POST',token,headers:{'Idempotency-Key':randomUUID()},body:{}}),200,r=>r.data.status==='AWAITING_USER'&&!r.data.account&&r.headers.get('cache-control')==='private, no-store'&&r.headers.get('referrer-policy')==='no-referrer');
    check(`${role}-convergent-second-key`,await request(prefix,{method:'POST',token,headers:{'Idempotency-Key':randomUUID()},body:{}}),200,r=>r.data.attempt_id===start.attempt_id);
    check(`${role}-other-owner-hidden`,await request(`${prefix}/${start.attempt_id}/resume`,{method:'POST',token:other.tokens.access_token,body:{}}),404);
    const opposite = role==='VENDOR'?'rider':'vendor';
    check(`${role}-other-application-role-hidden`,await request(`${gateway}/v1/${opposite}/bank-links/${start.attempt_id}/resume`,{method:'POST',token,body:{}}),404);
    check(`${role}-forged-subject-cannot-expand-owner`,await request(`${prefix}/${start.attempt_id}/resume`,{method:'POST',token:other.tokens.access_token,headers:{'X-Planext4u-Subject':identity.identity_id,'X-Planext4u-Session':'forged'},body:{}}),404);
    check(`${role}-readback-never-persists-hosted-url`,await request(`${prefix}/current`,{token}),200,r=>r.data.attempt_id===start.attempt_id&&!r.data.launch_url);
    const launch = new URL(start.launch_url);
    if (launch.origin !== 'http://127.0.0.1:18081' || !launch.pathname.startsWith('/synthetic/bank/')) throw new Error('Local fixture returned an unexpected launch destination.');
    check(`${role}-hosted-page-explicitly-synthetic`,await request(launch),200,r=>r.text.includes('Do not enter real bank details')&&r.headers.get('referrer-policy')==='no-referrer');
    check(`${role}-opening-page-does-not-verify`,await request(`${prefix}/${start.attempt_id}/resume`,{method:'POST',token,body:{}}),200,r=>r.data.status==='AWAITING_USER'&&!r.data.account);
    const completed = await fetch(`${launch.origin}${launch.pathname}`,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams({cap:launch.searchParams.get('cap'),result:'VERIFIED'}),redirect:'error',signal:AbortSignal.timeout(12000)});
    check(`${role}-explicit-hosted-test-action`,{status:completed.status},200);
    const verified = check(`${role}-owner-resolves-masked-proof`,await request(`${prefix}/${start.attempt_id}/resume`,{method:'POST',token,body:{}}),200,r=>r.data.status==='VERIFIED'&&r.data.account.last4==='1234'&&!r.data.launch_url&&!r.data.account.account_number);
    const proofBody = {tenant_id:identity.tenant_id,owner_id:identity.identity_id,country:'IN',...verified.account};
    // This public fixture credential belongs only to fake-provider. No external
    // bank credentials, accounts, transfers or paid requests are exercised.
    check(`${role}-independent-provider-proof`,await request('http://127.0.0.1:18081/v1/bank-accounts/verification:resolve',{method:'POST',token:'synthetic-local-bank-provider-only-token',body:proofBody}),200,r=>r.data.status==='VERIFIED'&&r.data.reference===verified.account.reference);
    check(`${role}-other-owner-independent-proof-denied`,await request('http://127.0.0.1:18081/v1/bank-accounts/verification:resolve',{method:'POST',token:'synthetic-local-bank-provider-only-token',body:{...proofBody,owner_id:other.identity_id}}),404);
    check(`${role}-verified-restart-readback`,await request(`${prefix}/current`,{token}),200,r=>r.data.status==='VERIFIED'&&r.data.account.reference===verified.account.reference&&!r.data.launch_url);
    if (role === 'RIDER') {
      const picture = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=', 'base64');
      const grant=check('RIDER-create-private-registration-identity',await request(`${gateway}/v1/media/uploads`,{method:'POST',token,body:{purpose:'IDENTITY_DOCUMENT',content_type:'image/png',size_bytes:picture.length,sha256:createHash('sha256').update(picture).digest('hex')}}),201);
      const uploadURL=new URL(grant.upload_url);
      if (uploadURL.origin!=='http://127.0.0.1:9000') throw new Error('Unexpected local private upload destination.');
      const put=await fetch(uploadURL,{method:'PUT',headers:grant.headers,body:picture,redirect:'error',signal:AbortSignal.timeout(12000)});
      check('RIDER-upload-private-identity-bytes',{status:put.status},200);
      check('RIDER-private-identity-actual-scanner-ready',await request(`${gateway}/v1/media/${grant.asset.id}/complete`,{method:'POST',token}),200,r=>r.data.state==='READY');
      const body={full_name:'Synthetic Bicycle Rider',vehicle_type:'BICYCLE',vehicle_number:'CYC1',documents:[{kind:'IDENTITY',asset_id:grant.asset.id}],bank_reference:verified.account.reference,zones:['600001']};
      check('RIDER-motor-vehicle-incomplete-evidence-denied',await request(`${gateway}/v1/rider/applications`,{method:'POST',token,headers:{'Idempotency-Key':randomUUID()},body:{...body,vehicle_type:'MOTORBIKE'}}),422);
      check('RIDER-normal-bicycle-draft-without-phone-mask-accepted',await request(`${gateway}/v1/rider/applications`,{method:'POST',token,headers:{'Idempotency-Key':randomUUID()},body}),201,r=>r.data.status==='KYC_REVIEW'&&r.data.bank_status==='PENDING_VERIFICATION'&&r.data.documents.length===1&&r.data.documents[0].ocr_status==='REVIEW_REQUIRED');
      check('RIDER-submitted-private-identity-retained',await request(`${gateway}/v1/media/${grant.asset.id}`,{method:'DELETE',token}),409,r=>r.data.error.code==='MEDIA_EVIDENCE_RETAINED');
    }
    check(`${role}-session-revoked`,await request(`${gateway}/v1/auth/revoke`,{method:'POST',token,body:{refresh_token:identity.tokens.refresh_token}}),204);
    check(`${role}-revoked-session-cannot-read-bank`,await request(`${prefix}/current`,{token}),401);
  }
} catch (error) {failure=error.message;}
finally {
  for (const tokens of sessions) {
    try {await request(`${gateway}/v1/auth/revoke`,{method:'POST',token:tokens.access_token,body:{refresh_token:tokens.refresh_token}});} catch {failure??='Synthetic session cleanup was unavailable.';}
  }
  const after=inventory(), sourceUnchanged=JSON.stringify(before)===JSON.stringify(after);
  if(!sourceUnchanged)failure??='Bank-link sources changed during runtime verification.';
  const report={check_id:'local-hosted-bank-links',result:failure?'FAILED':'PASSED',checked_at:new Date().toISOString(),source_unchanged:sourceUnchanged,files:before,checks,...(failure?{failure}:{}),note:'Persistent normal owner APIs and real local HTTP hosted flow. Provider result is explicitly synthetic. No real bank verification, external identity/device evidence, KYC approval or bank payout is claimed. Launch capabilities, tokens and masked account payloads are excluded from this receipt.'};
  writeFileSync(runFile,`${JSON.stringify(report,null,2)}\n`,{mode:0o600});
  console.log(JSON.stringify({result:report.result,check_count:checks.length,report:runFile,...(failure?{failure}:{})}));
  process.exitCode=failure?2:0;
}
