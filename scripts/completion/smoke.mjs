// Exercises real local services. Tokens/cookies and assertion keys never enter receipts.
import {readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {createHmac, createHash, randomUUID} from 'node:crypto';
import {spawnSync} from 'node:child_process';
import https from 'node:https';
import {setTimeout as delay} from 'node:timers/promises';
import {runtimeDirectory, loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';

const gateway = 'http://127.0.0.1:8082';
const admin = 'https://localhost:4173';
const tenant = loadLocalProfile('identity').TENANT_ID;
const otherTenant = 'bfc1e0db-73cf-40b3-9927-33590133da0b';
const ca = readFileSync(resolve(runtimeDirectory, 'localhost.crt'));
const secret = readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
const checks = [];
const sessions = [];
const runID = randomUUID();
const createdMedia = [];
const sourceOperatorFixtures = [];
function fixtureSQL(source) {
  const result = spawnSync('docker', ['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'], {encoding:'utf8',windowsHide:true,input:source,timeout:30000});
  if (result.status !== 0) throw new Error('Owned synthetic source-operator fixture operation failed.');
}
const cleanPicture = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=', 'base64');

async function privateDocument(headers, label) {
  const grant = check(`${label}-create-private-upload`, await request(`${gateway}/v1/media/uploads`, {method:'POST',headers,
    body:{purpose:'IDENTITY_DOCUMENT',content_type:'image/png',size_bytes:cleanPicture.length,sha256:createHash('sha256').update(cleanPicture).digest('hex')}}),201).data;
  createdMedia.push({id:grant.asset.id,headers});
  const uploadURL = new URL(grant.upload_url);
  if(uploadURL.origin !== 'http://127.0.0.1:9000') throw new Error('Local media signed an unexpected public destination');
  check(`${label}-unscanned-private-access-denied`,await request(`${gateway}/v1/media/${grant.asset.id}/access`,{method:'POST',headers}),409);
  const upload = await fetch(uploadURL,{method:'PUT',headers:grant.headers,body:cleanPicture,redirect:'error',signal:AbortSignal.timeout(10000)});
  check(`${label}-upload-actual-private-bytes`,{status:upload.status,data:{}},200);
  const overwrite = await fetch(uploadURL,{method:'PUT',headers:grant.headers,body:cleanPicture,redirect:'error',signal:AbortSignal.timeout(10000)});
  check(`${label}-signed-overwrite-denied`,{status:overwrite.status,data:{}},412);
  const unsigned = await fetch(`${uploadURL.origin}${uploadURL.pathname}`,{redirect:'error'});
  check(`${label}-anonymous-object-denied`,{status:unsigned.status,data:{}},403);
  check(`${label}-actual-scan-completed`,await request(`${gateway}/v1/media/${grant.asset.id}/complete`,{method:'POST',headers}),200,data=>data.id===grant.asset.id&&data.state==='READY');
  check(`${label}-different-owner-read-denied`,await request(`${gateway}/v1/media/${grant.asset.id}`,{headers:{Authorization:`Bearer ${roleTokensForMedia.customer}`}}),404);
  const accessResponse=await request(`${gateway}/v1/media/${grant.asset.id}/access`,{method:'POST',headers});
  // HTTP Date is the issuing server's clock at one-second resolution. Windows
  // and the Linux VM can differ by milliseconds after a VM pause/restart.
  const serverTime=Date.parse(accessResponse.headers?.date);
  const access=check(`${label}-ready-private-download-grant`,accessResponse,200,data=>data.asset_id===grant.asset.id&&Number.isFinite(serverTime)&&Date.parse(data.expires_at)>serverTime&&Date.parse(data.expires_at)-serverTime<121000).data;
  const download=await fetch(access.url,{redirect:'error'});
  const bytes=Buffer.from(await download.arrayBuffer());
  check(`${label}-private-download-exact-bytes`,{status:download.status,data:{}},200,()=>bytes.equals(cleanPicture)&&download.headers.get('cache-control')==='private, no-store');
  return grant.asset.id;
}
const roleTokensForMedia = {};

async function request(url, {method = 'GET', headers = {}, body} = {}) {
  const options = {method, headers: {'X-Correlation-ID':randomUUID(), ...headers}};
  if (body !== undefined) {
    options.body = JSON.stringify(body);
    options.headers['Content-Type'] = 'application/json';
  }
  if (url.startsWith(admin)) {
    return new Promise((resolveRequest, reject) => {
      const req = https.request(url, {...options, ca, family: 4, timeout: 10000}, response => {
        let text = '';
        response.on('data', chunk => {text += chunk;});
        response.on('end', () => {
          let data;
          try {data = JSON.parse(text);} catch {data = {};}
          resolveRequest({status: response.statusCode, data, text, headers: response.headers});
        });
      });
      req.on('error', reject);
      req.on('timeout', () => req.destroy(new Error('Local HTTPS timeout')));
      if (options.body) req.write(options.body);
      req.end();
    });
  }
  const response = await fetch(url, {...options, signal: AbortSignal.timeout(10000)});
  const text=await response.text();
  let data;
  try {data = JSON.parse(text);} catch {data = {};}
  return {status: response.status, data, text, headers: Object.fromEntries(response.headers)};
}

function check(id, response, status, predicate = () => true) {
  if(response.headers?.['x-correlation-id']?.includes(',')) throw new Error(`${id}: duplicate correlation response headers`);
  const passed = response.status === status && predicate(response.data);
  checks.push({id, result: passed ? 'PASSED' : 'FAILED', status: response.status,
    ...(response.data?.error?.code ? {error_code: response.data.error.code} : {})});
  if (!passed) throw new Error(`${id}: HTTP ${response.status}; expected ${status}`);
  return response;
}

async function assertion(subject, {roles = ['SUPER_ADMIN'], tenantID = tenant, methods = ['pwd','totp'], expected = 201, subjectID = randomUUID(), bindSource = true} = {}) {
  const body = {subject_id: subjectID, session_id: randomUUID(), tenant_id: tenantID,
    display_name: 'Synthetic Phase 0 verification', roles, allowed_countries: ['IN'], selected_country: 'IN',
    authenticated_at: new Date().toISOString(), auth_methods: methods};
  const timestamp = String(Math.floor(Date.now() / 1000));
  const signature = createHmac('sha256', secret).update(`v1\n${timestamp}\n${JSON.stringify(body)}`).digest('hex');
  const result = check(`admin-exchange-${subject}`, await request('http://127.0.0.1:8090/internal/v1/admin-sessions', {
    method: 'POST', body, headers: {'X-P4U-Admin-Timestamp': timestamp, 'X-P4U-Admin-Signature': `v1=${signature}`},
  }), expected);
  if (expected !== 201) return;
  if (bindSource) {
    // Explicit synthetic infrastructure. Production Firebase sign-in derives
    // this revision from the directory; the older signed local exchange does not.
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    if (![tenantID,subjectID,body.session_id].every(id=>uuid.test(id)) || !roles.every(role=>/^[A-Z_]+$/.test(role))) throw new Error('Invalid owned source fixture scope.');
    const provider = `synthetic-smoke-${runID}-${subjectID}`;
    fixtureSQL(`BEGIN; INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenantID}','${subjectID}','${provider}','Synthetic API acceptance operator',ARRAY[${roles.map(role=>`'${role}'`).join(',')}],ARRAY['IN']); UPDATE admin.sessions SET operator_revision=1 WHERE session_id='${body.session_id}' AND tenant_id='${tenantID}' AND subject_id='${subjectID}'; COMMIT;`);
    sourceOperatorFixtures.push({tenant:tenantID,subject:subjectID,provider});
  }
  const cookie = result.headers['set-cookie']?.split(';')[0];
  if (!cookie || !/Secure/i.test(result.headers['set-cookie']) || !/HttpOnly/i.test(result.headers['set-cookie'])) throw new Error('Expected secure HTTP-only administrator cookie');
  const view = check(`admin-session-${subject}`, await request(`${admin}/admin/api/v1/session`, {headers: {Cookie: cookie}}), 200,
    data => data.assurance?.mfa_satisfied === true);
  const session = {Cookie: cookie, Origin: admin, 'X-CSRF-Token': view.data.csrf_token};
  sessions.push(session);
  return session;
}

let failure;
try {
  check('anonymous-mobile-denied', await request(`${gateway}/v1/me`), 401);
  check('anonymous-admin-denied', await request(`${admin}/admin/api/v1/session`), 401);
  const roleTokens = {};
  for (const role of ['customer','vendor','rider']) {
    const login = check(`${role}-provider-exchange`, await request(`${gateway}/v1/auth/exchange`, {method: 'POST',
      body: {provider: 'local', provider_token: `synthetic-${role}`, device_id: `phase0-${role}`, country: 'IN'}}), 201,
      data => typeof data.tokens?.access_token === 'string');
    roleTokens[role] = login.data.tokens.access_token;
    roleTokensForMedia[role] = roleTokens[role];
    check(`${role}-identity`, await request(`${gateway}/v1/me`, {headers: {Authorization: `Bearer ${roleTokens[role]}`}}), 200,
      data => data.roles?.includes(role.toUpperCase()) && data.tenant_id === tenant);
  }
  for (const [role, path] of [['customer','/v1/vendor/application'], ['vendor','/v1/rider/profile'], ['rider','/v1/vendor/application']]) {
    check(`${role}-wrong-role-denied`, await request(`${gateway}${path}`, {headers: {Authorization: `Bearer ${roleTokens[role]}`}}), 403);
  }
  await assertion('without-mfa', {methods: ['pwd'], expected: 403});
  const unboundSource = await assertion('unbound-source', {bindSource:false});
  check('organization-root-missing-source-proof-denied', await request(`${admin}/admin/api/v1/organization/nodes`, {method:'POST',headers:unboundSource,body:{name:'Unbound synthetic source',parent_id:'',kind:'ORGANIZATION'}}),403);
  const mobileHeaders = {Authorization:`Bearer ${roleTokens.customer}`};
  check('gateway-pages-reach-configuration-owner',await request(`${gateway}/v1/pages/unpublished-${runID}?locale=en`,{headers:mobileHeaders}),404,data=>data.error?.code==='PAGE_NOT_FOUND');
  check('unconfigured-linking-provider-fails-closed',await request(`${gateway}/v1/me/sign-in-methods/verify`,{method:'POST',headers:mobileHeaders,body:{provider_token:'synthetic-linking-token'}}),503);
  check('linking-client-cannot-select-provider-uid',await request(`${gateway}/v1/me/sign-in-methods/binding`,{method:'POST',headers:mobileHeaders,body:{provider_token:'synthetic-linking-token',provider_subject:'foreign'}}),422);
  const initialBootstrap = await request(`${gateway}/v1/bootstrap?platform=android&app_version=0.1.0&locale=en`, {headers:mobileHeaders});
  if (![200,404].includes(initialBootstrap.status)) check('initial-bootstrap',initialBootstrap,200);
  // First run initializes the local mobile tenant. Subsequent runs publish in a
  // fresh synthetic tenant, preserving the existing mobile configuration.
  const publicationTenant = initialBootstrap.status===404 ? tenant : randomUUID();
  const runFlag = `phase0_run_${runID.replaceAll('-','')}`;
  const maker = await assertion('maker', {tenantID:publicationTenant});
  const checker = await assertion('checker', {tenantID:publicationTenant});
  const outsider = await assertion('other-tenant', {tenantID: otherTenant});
  // Phase 2 uses real owner/BFF paths and fresh synthetic identities. It does
  // not turn provider claims into platform administrator or approval status.
  for(const role of ['VENDOR','RIDER']) {
    const body={provider:'local',provider_token:`synthetic-participant-${randomUUID()}`,device_id:`phase2-${runID}`,country:'IN',enroll_as:role};
    const enrollment=check(`fresh-${role}-enrollment`,await request(`${gateway}/v1/auth/enroll`,{method:'POST',body}),201,data=>data.roles?.includes(role)&&!data.roles?.includes('ADMIN'));
    check(`fresh-${role}-current-identity`,await request(`${gateway}/v1/me`,{headers:{Authorization:`Bearer ${enrollment.data.tokens.access_token}`}}),200,data=>data.id===enrollment.data.identity_id);
    const evidenceHeaders={Authorization:`Bearer ${enrollment.data.tokens.access_token}`,'Idempotency-Key':`evidence-${runID}-${role}`};
    if(role==='VENDOR') {
      const application=check('fresh-vendor-normal-application',await request(`${gateway}/v1/vendor/applications`,{method:'POST',headers:evidenceHeaders,body:{business_name:'Synthetic private evidence verification',business_type:'SOLE_PROPRIETOR',contact_name:'Synthetic owner'}}),201,data=>Array.isArray(data.documents)&&Array.isArray(data.service_zones)&&Array.isArray(data.timeline)&&Array.isArray(data.allowed_actions)).data;
      check('fresh-vendor-cannot-submit-unverified-private-ids',await request(`${gateway}/v1/vendor/application/documents`,{method:'POST',headers:{...evidenceHeaders,'If-Match':`"${application.revision}"`},body:{documents:[{kind:'OWNER_IDENTITY',asset_id:randomUUID()},{kind:'BUSINESS_REGISTRATION',asset_id:randomUUID()}]}}),403);
      check('fresh-vendor-rejection-preserves-application',await request(`${gateway}/v1/vendor/application`,{headers:evidenceHeaders}),200,data=>data.revision===application.revision&&data.status==='REGISTERED');
      const identity=await privateDocument(evidenceHeaders,'vendor-identity');
      const registration=await privateDocument(evidenceHeaders,'vendor-business');
      check('fresh-vendor-scanned-private-documents-submitted',await request(`${gateway}/v1/vendor/application/documents`,{method:'POST',headers:{...evidenceHeaders,'If-Match':`"${application.revision}"`},body:{documents:[{kind:'OWNER_IDENTITY',asset_id:identity},{kind:'BUSINESS_REGISTRATION',asset_id:registration}]}}),200,data=>data.status==='DOCUMENTS_SUBMITTED'&&data.documents?.length===2&&data.documents.every(document=>document.ocr_status==='PENDING'&&!document.extracted_fields));
    } else {
      check('fresh-rider-cannot-register-unverified-private-ids',await request(`${gateway}/v1/rider/applications`,{method:'POST',headers:evidenceHeaders,body:{full_name:'Synthetic private evidence rider',phone_masked:'****1234',vehicle_type:'BIKE',vehicle_number:'TN01AB1234',documents:[{kind:'IDENTITY',asset_id:randomUUID()},{kind:'DRIVER_LICENSE',asset_id:randomUUID()},{kind:'VEHICLE_REGISTRATION',asset_id:randomUUID()},{kind:'INSURANCE',asset_id:randomUUID()}],bank_reference:'bankref_synthetic0001',zones:['600001']}}),403);
      check('fresh-rider-rejection-does-not-create-profile',await request(`${gateway}/v1/rider/profile`,{headers:evidenceHeaders}),404);
      const identity=await privateDocument(evidenceHeaders,'rider-identity');
      const license=await privateDocument(evidenceHeaders,'rider-license');
      const vehicleRegistration=await privateDocument(evidenceHeaders,'rider-registration');
      const insurance=await privateDocument(evidenceHeaders,'rider-insurance');
      check('fresh-rider-scanned-private-documents-submitted',await request(`${gateway}/v1/rider/applications`,{method:'POST',headers:evidenceHeaders,body:{full_name:'Synthetic private evidence rider',phone_masked:'****1234',vehicle_type:'BIKE',vehicle_number:'TN01AB1234',documents:[{kind:'IDENTITY',asset_id:identity},{kind:'DRIVER_LICENSE',asset_id:license},{kind:'VEHICLE_REGISTRATION',asset_id:vehicleRegistration},{kind:'INSURANCE',asset_id:insurance}],bank_reference:'bankref_synthetic0001',zones:['600001']}}),201,data=>data.status==='KYC_REVIEW'&&data.bank_status==='PENDING_VERIFICATION'&&data.documents?.every(document=>document.ocr_status==='REVIEW_REQUIRED'));
    }
    for(const asset of createdMedia.filter(asset=>asset.headers===evidenceHeaders)) {
      check(`${role}-application-evidence-deletion-rejected`,await request(`${gateway}/v1/media/${asset.id}`,{method:'DELETE',headers:evidenceHeaders}),409,data=>data.error?.code==='MEDIA_EVIDENCE_RETAINED');
      check(`${role}-application-evidence-remains-ready`,await request(`${gateway}/v1/media/${asset.id}`,{headers:evidenceHeaders}),200,data=>data.state==='READY');
    }
    const unsubmitted=await privateDocument(evidenceHeaders,`${role}-unsubmitted`);
    check(`${role}-unsubmitted-media-deleted`,await request(`${gateway}/v1/media/${unsubmitted}`,{method:'DELETE',headers:evidenceHeaders}),204);
    check(`${role}-unsubmitted-media-cleanup-retry`,await request(`${gateway}/v1/media/${unsubmitted}`,{method:'DELETE',headers:evidenceHeaders}),204);
    check(`${role}-unsubmitted-media-hidden`,await request(`${gateway}/v1/media/${unsubmitted}`,{headers:evidenceHeaders}),404);
    const consentURL=`${gateway}/v1/me/consents/ANALYTICS`;
    check(`${role}-grant-needs-observed-version`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:true,policy_version:'local-consent-order-1'}}),422);
    const initial=check(`${role}-explicit-consent-grant`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:true,policy_version:'local-consent-order-1',expected_version:0}}),200,data=>data.granted&&data.version===1).data;
    const denial=check(`${role}-unconditional-withdrawal`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:false,policy_version:'local-consent-order-1'}}),200,data=>!data.granted&&data.version===2).data;
    check(`${role}-stale-grant-cannot-reverse-withdrawal`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:true,policy_version:'local-consent-order-1',expected_version:initial.version}}),409,data=>data.error?.code==='CONSENT_VERSION_CONFLICT');
    check(`${role}-withdrawal-survives-stale-grant`,await request(`${gateway}/v1/me/consents`,{headers:evidenceHeaders}),200,data=>data.consents?.some(consent=>consent.purpose==='ANALYTICS'&&!consent.granted&&consent.version===denial.version));
    check(`${role}-refreshed-explicit-consent-grant`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:true,policy_version:'local-consent-order-1',expected_version:denial.version}}),200,data=>data.granted&&data.version===3);
    check(`${role}-withdrawal-with-stale-precondition-still-applies`,await request(consentURL,{method:'PUT',headers:evidenceHeaders,body:{granted:false,policy_version:'local-consent-order-1',expected_version:0}}),200,data=>!data.granted&&data.version===4);
    const portable=check(`${role}-portable-identity-history`,await request(`${gateway}/v1/me/data-export`,{headers:evidenceHeaders}),200,data=>data.schema_version===1&&data.identity_id===enrollment.data.identity_id&&data.consent_history?.length===4&&data.consent_history.every((item,index)=>item.version===index+1&&item.country==='IN'&&item.granted===(index%2===0)));
    check(`${role}-export-explicit-coverage`,portable,200,data=>data.coverage?.included?.includes('identity.consent_history')&&data.coverage?.excluded?.includes('orders_and_payments'));
    check(`${role}-export-private-no-credentials`,portable,200,()=>portable.headers?.['cache-control']==='no-store'&&!portable.text.includes(enrollment.data.tokens.access_token)&&!portable.text.includes(enrollment.data.tokens.refresh_token)&&!portable.text.includes(body.device_id)&&!portable.text.includes(body.provider_token)&&!(/"(?:provider_subject|refresh_digest|device_id)"\s*:/.test(portable.text)));
    check(`${role}-export-query-cannot-select-other-account`,await request(`${gateway}/v1/me/data-export?identity_id=foreign-account&country=NG`,{headers:evidenceHeaders}),422,data=>data.error?.code==='VALIDATION_FAILED');
    check(`fresh-${role}-revoke`,await request(`${gateway}/v1/auth/revoke`,{method:'POST',body:{refresh_token:enrollment.data.tokens.refresh_token}}),204);
    check(`${role}-revoked-export-denied`,await request(`${gateway}/v1/me/data-export`,{headers:evidenceHeaders}),401);
    check(`fresh-${role}-revoked-access-token-denied`,await request(`${gateway}/v1/vendor/application`,{headers:{Authorization:`Bearer ${enrollment.data.tokens.access_token}`}}),401);
  }
  check('participant-cannot-enroll-admin',await request(`${gateway}/v1/auth/enroll`,{method:'POST',body:{provider:'local',provider_token:'synthetic-customer',device_id:`phase2-${runID}`,country:'IN',enroll_as:'ADMIN'}}),422);
  const lifecycleInput={provider:'local',provider_token:`synthetic-participant-${randomUUID()}`,device_id:`lifecycle-${runID}`,country:'IN'};
  const lifecycleLogin=check('lifecycle-fresh-account',await request(`${gateway}/v1/auth/exchange`,{method:'POST',body:lifecycleInput}),201).data;
  const lifecycleHeaders={Authorization:`Bearer ${lifecycleLogin.tokens.access_token}`};
  check('lifecycle-initial-active',await request(`${gateway}/v1/me/lifecycle`,{headers:lifecycleHeaders}),200,data=>data.status==='ACTIVE'&&!data.deletion);
  check('lifecycle-deactivation-confirmation-required',await request(`${gateway}/v1/me/deactivation`,{method:'POST',headers:lifecycleHeaders,body:{confirmation:'incorrect'}}),422);
  const deletion=check('lifecycle-schedule-deletion',await request(`${gateway}/v1/me/deletion-requests`,{method:'POST',headers:lifecycleHeaders,body:{confirmation:'DELETE MY ACCOUNT',reason:'Synthetic local recovery verification'}}),202,data=>data.status==='SCHEDULED').data;
  check('lifecycle-scheduled-deletion-visible',await request(`${gateway}/v1/me/lifecycle`,{headers:lifecycleHeaders}),200,data=>data.deletion?.id===deletion.id);
  check('lifecycle-cancel-deletion',await request(`${gateway}/v1/me/deletion-requests/${deletion.id}/cancel`,{method:'POST',headers:lifecycleHeaders,body:{confirmation:'CANCEL DELETION'}}),204);
  check('lifecycle-cancellation-persisted',await request(`${gateway}/v1/me/lifecycle`,{headers:lifecycleHeaders}),200,data=>data.status==='ACTIVE'&&!data.deletion);
  check('lifecycle-self-deactivate',await request(`${gateway}/v1/me/deactivation`,{method:'POST',headers:lifecycleHeaders,body:{confirmation:'DEACTIVATE MY ACCOUNT'}}),204);
  check('lifecycle-disabled-token-denied',await request(`${gateway}/v1/me/lifecycle`,{headers:lifecycleHeaders}),401);
  check('lifecycle-disabled-refresh-denied',await request(`${gateway}/v1/auth/refresh`,{method:'POST',body:{refresh_token:lifecycleLogin.tokens.refresh_token}}),401);
  check('lifecycle-ordinary-login-stays-disabled',await request(`${gateway}/v1/auth/exchange`,{method:'POST',body:lifecycleInput}),403);
  const recovered=check('lifecycle-explicit-provider-recovery',await request(`${gateway}/v1/auth/recover`,{method:'POST',body:lifecycleInput}),201,data=>data.identity_id===lifecycleLogin.identity_id).data;
  check('lifecycle-recovered-account-active',await request(`${gateway}/v1/me/lifecycle`,{headers:{Authorization:`Bearer ${recovered.tokens.access_token}`}}),200,data=>data.status==='ACTIVE'&&!data.deletion);
  check('lifecycle-old-token-still-denied-after-recovery',await request(`${gateway}/v1/me`,{headers:lifecycleHeaders}),401);
  check('lifecycle-recovered-session-revoke',await request(`${gateway}/v1/auth/revoke`,{method:'POST',body:{refresh_token:recovered.tokens.refresh_token}}),204);
  const createNode=async(headers,name,parent_id='')=>check(`organization-create-${name}`,await request(`${admin}/admin/api/v1/organization/nodes`,{method:'POST',headers,body:{name,parent_id,kind:parent_id?'TERRITORY':'ORGANIZATION'}}),201).data;
  const root=await createNode(maker,`Phase 2 ${runID}`);
  const north=await createNode(maker,'North territory',root.id);
  const south=await createNode(maker,'South territory',root.id);
  await createNode(maker,'North district',north.id);
  await createNode(outsider,`Other tenant ${runID}`);
  const role=check('organization-create-job-role',await request(`${admin}/admin/api/v1/organization/roles`,{method:'POST',headers:maker,body:{node_id:root.id,name:'Territory manager',capabilities:['organization.read','organization.manage','staff.manage','resource.read','resource.export','resource.command']}}),201).data;
  const regionalID=randomUUID();
  const assignment=check('organization-assign-regional-staff',await request(`${admin}/admin/api/v1/organization/staff`,{method:'PUT',headers:maker,body:{subject_id:regionalID,node_id:north.id,role_id:role.id,status:'ACTIVE',revision:0}}),200).data;
  const regional=await assertion('regional',{roles:['REGIONAL_STAFF'],tenantID:publicationTenant,subjectID:regionalID});
  check('organization-inherited-north-only',await request(`${admin}/admin/api/v1/organization`,{headers:regional}),200,data=>data.nodes?.length===2&&data.nodes.every(node=>node.name.startsWith('North')));
  check('organization-search-cannot-find-south',await request(`${admin}/admin/api/v1/organization?search=South`,{headers:regional}),200,data=>data.nodes?.length===0);
  const exported=await request(`${admin}/admin/api/v1/organization/export`,{headers:regional});
  check('organization-export-north-only',exported,200,()=>exported.text.includes(north.id)&&!exported.text.includes(south.id));
  check('organization-direct-south-command-denied',await request(`${admin}/admin/api/v1/organization/nodes`,{method:'POST',headers:regional,body:{parent_id:south.id,name:'Denied child',kind:'TERRITORY'}}),403);
  check('organization-client-authority-field-rejected',await request(`${admin}/admin/api/v1/organization/nodes`,{method:'POST',headers:regional,body:{parent_id:south.id,name:'Denied override',kind:'TERRITORY',platform_admin:true}}),422);
  check('organization-cross-tenant-parent-denied',await request(`${admin}/admin/api/v1/organization/nodes`,{method:'POST',headers:outsider,body:{parent_id:north.id,name:'Denied foreign child',kind:'TERRITORY'}}),403);
  check('organization-regional-global-command-denied',await request(`${admin}/admin/api/v1/operations`,{method:'POST',headers:regional,body:{domain:'CATALOG',action:'UPSERT',target_id:`denied-${runID}`,reason:'Synthetic scope denial',payload:{}}}),403);
  const inviteeID=randomUUID();
  const invitation=check('organization-create-intended-invitation',await request(`${admin}/admin/api/v1/organization/invitations`,{method:'POST',headers:regional,body:{subject_id:inviteeID,node_id:north.id,role_id:role.id}}),201).data;
  const invitee=await assertion('invited-regional',{roles:['REGIONAL_STAFF'],tenantID:publicationTenant,subjectID:inviteeID});
  check('organization-invitee-no-initial-scope',await request(`${admin}/admin/api/v1/organization`,{headers:invitee}),403);
  check('organization-invitation-cannot-be-stolen',await request(`${admin}/admin/api/v1/organization/invitations/accept`,{method:'POST',headers:regional,body:{token:invitation.token}}),403);
  check('organization-invitee-accepts',await request(`${admin}/admin/api/v1/organization/invitations/accept`,{method:'POST',headers:invitee,body:{token:invitation.token}}),204);
  check('organization-invitation-replay-denied',await request(`${admin}/admin/api/v1/organization/invitations/accept`,{method:'POST',headers:invitee,body:{token:invitation.token}}),409);
  check('organization-invitee-current-scope',await request(`${admin}/admin/api/v1/organization`,{headers:invitee}),200,data=>data.nodes?.length===2);
  const statusURL=`${admin}/admin/api/v1/organization/nodes/${north.id}/status`;
  check('organization-regional-cannot-disable-territory',await request(statusURL,{method:'PUT',headers:regional,body:{revision:north.revision,active:false}}),403);
  check('organization-status-value-required',await request(statusURL,{method:'PUT',headers:maker,body:{revision:north.revision}}),422);
  check('organization-disable-territory',await request(statusURL,{method:'PUT',headers:maker,body:{revision:north.revision,active:false}}),204);
  check('organization-disabled-territory-revokes-old-cookie-scope',await request(`${admin}/admin/api/v1/organization`,{headers:regional}),403);
  check('organization-super-can-inspect-disabled-territory',await request(`${admin}/admin/api/v1/organization`,{headers:maker}),200,data=>data.nodes?.some(node=>node.id===north.id&&!node.active&&node.revision===north.revision+1));
  check('organization-stale-territory-status-denied',await request(statusURL,{method:'PUT',headers:maker,body:{revision:north.revision,active:true}}),409);
  check('organization-reactivate-territory',await request(statusURL,{method:'PUT',headers:maker,body:{revision:north.revision+1,active:true}}),204);
  check('organization-reactivation-restores-current-staff-scope',await request(`${admin}/admin/api/v1/organization`,{headers:regional}),200,data=>data.nodes?.length===2);
  check('organization-suspend-staff',await request(`${admin}/admin/api/v1/organization/staff`,{method:'PUT',headers:maker,body:{...assignment,status:'SUSPENDED'}}),200);
  check('organization-old-cookie-loses-scope',await request(`${admin}/admin/api/v1/organization`,{headers:regional}),403);
  // Phase 1: unsupported business commands must fail at the real BFF while
  // preserving four-eyes approval and pending state, with no financial fixture.
  check('unsupported-catalog-adapter-explicit',await request(`${admin}/admin/api/v1/operations`,{method:'POST',headers:maker,
    body:{domain:'CATALOG',action:'UPSERT',target_id:`unavailable-${runID}`,reason:'Synthetic local unsupported adapter verification',payload:{name:'Synthetic unavailable item'}}}),503,data=>data.error?.code==='ADMIN_ADAPTER_UNAVAILABLE');
  const unavailableWallet=check('unsupported-wallet-awaits-approval',await request(`${admin}/admin/api/v1/operations`,{method:'POST',headers:maker,
    body:{domain:'WALLET',action:'ADJUST',target_id:`unavailable-${runID}`,reason:'Synthetic local unsupported wallet verification',payload:{points:1}}}),201,data=>data.status==='PENDING_APPROVAL');
  const unavailableApproval=`${admin}/admin/api/v1/operations/${unavailableWallet.data.id}/approve`;
  check('unsupported-wallet-self-approval-denied',await request(unavailableApproval,{method:'POST',headers:maker,body:{expected_revision:unavailableWallet.data.revision}}),409);
  for(let attempt=0;attempt<2;attempt++) check(`unsupported-wallet-adapter-explicit-${attempt}`,await request(unavailableApproval,{method:'POST',headers:checker,body:{expected_revision:unavailableWallet.data.revision}}),503,data=>data.error?.code==='ADMIN_ADAPTER_UNAVAILABLE');

  const auditor = await assertion('auditor', {roles: ['AUDITOR']});
  check('country-outside-session-denied', await request(`${admin}/admin/api/v1/session/country`, {method: 'PUT', headers: maker, body: {country: 'US'}}), 403);
  check('auditor-cms-denied', await request(`${admin}/admin/api/v1/cms/workspace`, {headers: auditor}), 403);
  const old = await request(`${admin}/admin/api/v1/cms/workspace`, {headers: maker});
  if (![200,404].includes(old.status)) check('existing-workspace', old, 200);
  // Initialize only an absent synthetic workspace; never overwrite an existing draft.
  const configurationURL = 'http://127.0.0.1:8084/v1/bootstrap?platform=android&app_version=0.1.0&locale=en';
  const configurationHeaders = {'X-Planext4u-Tenant':publicationTenant,'X-Planext4u-Country':'IN'};
  const before = await request(configurationURL, {headers:configurationHeaders});
  if (before.status === 404) {
    if (old.status === 200 && !old.data.workspace?.flags?.phase0_reference) throw new Error('An existing workspace draft requires deliberate publication; it was preserved.');
    const workspace = {minimum_versions: {ANDROID:'0.1.0',IOS:'0.1.0',WEB:'0.1.0'}, latest_versions: {ANDROID:'0.1.0',IOS:'0.1.0',WEB:'0.1.0'},
      supported_locales: ['en','ta'], default_locale: 'en', consent_policies: [],
      flags: {customer_home:true,vendor_home:true,rider_home:true,phase0_reference:true,[runFlag]:true},
      home_sections: [{id:'phase0-hero',kind:'HERO',title_key:'home.hero',enabled:true,priority:10}]};
    check('admin-csrf-denied', await request(`${admin}/admin/api/v1/cms/workspace/draft`, {method:'PUT', headers:{Cookie:maker.Cookie,Origin:admin}, body:{expected_revision:0,workspace}}), 403);
    const saved = check('workspace-draft', await request(`${admin}/admin/api/v1/cms/workspace/draft`, {method:'PUT',headers:maker,body:{expected_revision:old.status===200?old.data.revision:0,workspace}}), 200);
    const change = check('cms-publication-request', await request(`${admin}/admin/api/v1/operations`, {method:'POST',headers:maker,
      body:{domain:'CMS',action:'PUBLISH',target_id:'workspace',reason:`Synthetic Phase 0 bootstrap ${runID}`,
        payload:{publication_type:'WORKSPACE',expected_draft_revision:saved.data.revision,expected_snapshot_revision:0}}}), 201,
      data => data.status === 'PENDING_APPROVAL');
    const approvePath = `${admin}/admin/api/v1/operations/${change.data.id}/approve`;
    check('cross-tenant-approval-denied', await request(approvePath,{method:'POST',headers:outsider,body:{expected_revision:change.data.revision}}),404);
    check('maker-cannot-self-approve', await request(approvePath,{method:'POST',headers:maker,body:{expected_revision:change.data.revision}}),409);
    check('second-admin-publishes', await request(approvePath,{method:'POST',headers:checker,body:{expected_revision:change.data.revision}}),200,data=>data.status==='EXECUTED');
    check('published-workspace-owner-read',await request(configurationURL,{headers:configurationHeaders}),200,data=>data.flags?.[runFlag]===true);
  }
  const bootstrap = check('published-mobile-bootstrap',await request(`${gateway}/v1/bootstrap?platform=android&app_version=0.1.0&locale=en`,{headers:mobileHeaders}),200,
    data=>data.revision>0 && Array.isArray(data.home_sections)&&Array.isArray(data.consent_policies)&&Array.isArray(data.pages));
  // Onboarding publication uses the real draft/four-eyes owner path in this
  // run's fresh synthetic tenant. Existing mobile-tenant configuration remains.
  if(publicationTenant!==tenant) {
    let snapshotRevision=(await request(configurationURL,{headers:configurationHeaders})).data.revision;
    const educationPages=[];
    for(const role of ['CUSTOMER','VENDOR','RIDER']) {
      const pageID=`mobile-onboarding-${role.toLowerCase()}-ta`;
      const page={id:pageID,route:`/app/onboarding/${role.toLowerCase()}/ta`,title_key:`onboarding.${role.toLowerCase()}.ta`,audience:[role],enabled:true,
        blocks:[{id:'welcome',kind:'ONBOARDING',enabled:true,priority:0,content:{version:'1',title:'வணக்கம்',message:'Choose when to share device permissions.'}}]};
      const saved=check(`${role}-education-draft`,await request(`${admin}/admin/api/v1/cms/pages/${pageID}/draft`,{method:'PUT',headers:maker,body:{expected_revision:0,page}}),200).data;
      const change=check(`${role}-education-awaits-independent-approval`,await request(`${admin}/admin/api/v1/operations`,{method:'POST',headers:maker,
        body:{domain:'CMS',action:'PUBLISH',target_id:pageID,reason:'Synthetic local role education verification',payload:{publication_type:'PAGE',expected_draft_revision:saved.revision,expected_snapshot_revision:snapshotRevision}}}),201,data=>data.status==='PENDING_APPROVAL').data;
      const approve=`${admin}/admin/api/v1/operations/${change.id}/approve`;
      check(`${role}-education-self-approval-denied`,await request(approve,{method:'POST',headers:maker,body:{expected_revision:change.revision}}),409);
      check(`${role}-education-independent-publication`,await request(approve,{method:'POST',headers:checker,body:{expected_revision:change.revision}}),200,data=>data.status==='EXECUTED');
      snapshotRevision++;
      educationPages.push({role,pageID});
    }
    // The configuration owner uses its documented default 60-second local
    // process cache. Independent publication invalidates the admin process;
    // require visibility within that existing budget, rather than assuming
    // synchronous cache invalidation across two separate processes.
    const started=Date.now();let visible;
    do {
      visible=await request(configurationURL,{headers:configurationHeaders});
      if(visible.status!==200||visible.data.revision>=snapshotRevision)break;
      await delay(250);
    }while(Date.now()-started<65000);
    check('education-publication-visible-within-cache-budget',visible,200,data=>data.revision>=snapshotRevision);
    for(const {role,pageID} of educationPages) {
      const pageURL=`http://127.0.0.1:8084/v1/pages/${pageID}?locale=ta`;
      check(`${role}-education-owner-read`,await request(pageURL,{headers:{...configurationHeaders,'X-Planext4u-Roles':role}}),200,data=>data.page?.blocks?.[0]?.content?.title==='வணக்கம்');
      check(`${role}-education-foreign-audience-hidden`,await request(pageURL,{headers:{...configurationHeaders,'X-Planext4u-Roles':role==='CUSTOMER'?'RIDER':'CUSTOMER'}}),404);
      check(`${role}-education-query-authority-denied`,await request(`${pageURL}&roles=${role}&audience=${role}`,{headers:configurationHeaders}),404);
      check(`${role}-education-other-country-hidden`,await request(pageURL,{headers:{...configurationHeaders,'X-Planext4u-Roles':role,'X-Planext4u-Country':'GB'}}),404);
    }
    for(const role of ['CUSTOMER','VENDOR','RIDER','GUEST'])check(`${role}-bootstrap-audience-filter`,await request(configurationURL,{headers:{...configurationHeaders,'X-Planext4u-Roles':role}}),200,data=>data.pages.every(page=>page.audience.includes(role)||page.audience.includes('PUBLIC')));
  }
  check('client-tenant-header-cannot-override-jwt',await request(`${gateway}/v1/bootstrap?platform=android&app_version=0.1.0&locale=en`,
    {headers:{...mobileHeaders,'X-Planext4u-Tenant':publicationTenant===tenant?otherTenant:publicationTenant}}),200,
    data=>data.revision===bootstrap.data.revision && (publicationTenant===tenant || !data.flags?.[runFlag]));
} catch (error) {failure=error.message;}
finally {
  for (const headers of sessions) {
    try {check('admin-session-revoked',await request(`${admin}/admin/api/v1/session`,{method:'DELETE',headers}),204);
      check('revoked-admin-denied',await request(`${admin}/admin/api/v1/session`,{headers}),401);} catch(error) {failure??=error.message;}
  }
  for (const fixture of sourceOperatorFixtures) {
    try {fixtureSQL(`UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${fixture.tenant}' AND subject_id='${fixture.subject}' AND provider_subject='${fixture.provider}';`);} catch(error) {failure??=error.message;}
  }
  const report={check_id:'local-role-smoke',checked_at:new Date().toISOString(),result:failure?'FAILED':'PASSED',checks,
    ...(failure?{failure}:{}),note:'Real persistent local API/BFF requests with synthetic provider identities and signed local MFA assertions. Only fresh synthetic operator fixtures bind the source directory revision and are retired after session revocation. Production Firebase derives revisions itself. No external authenticator challenge or mobile device journey is claimed. No financial seed/reset was run.'};
  writeFileSync(resolve(stackDirectory,'smoke.json'),`${JSON.stringify(report,null,2)}\n`);
  console.log(JSON.stringify(report,null,2));
  process.exitCode=failure?2:0;
}
