// Opt-in local-only KYC boundary check. All identities, documents and decisions
// are synthetic. This proves wiring and failure behavior, not real KYC approval.
import {createHash, createHmac, randomUUID} from 'node:crypto';
import {mkdirSync, mkdtempSync, readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {requireLocalEngine} from './verify.mjs';

const kycToken='synthetic-local-kyc-provider-only-token',ocrToken='synthetic-local-ocr-provider-only-token';
const origin='https://localhost:4173';
const tenant=loadLocalProfile('identity').TENANT_ID;
const run=randomUUID(), checks=[];
const picture=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=','base64');
let output, failure, operator, participant;
function check(id,ok,details){checks.push({id,result:ok?'PASSED':'FAILED',...(details?{details}:{})});if(!ok)throw Error(`KYC boundary failed: ${id}`);}
function docker(args,options={}){return spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});}
function sql(source){const value=docker(['exec','-i','planext4u-phase0-postgres-1','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});if(value.status!==0)throw Error('Synthetic KYC fixture SQL failed');}
async function request(base,path,{method='GET',body,headers={}}={}){
 const response=await fetch(base+path,{method,headers:{'Content-Type':'application/json',...headers},...(body===undefined?{}:{body:JSON.stringify(body)}),redirect:'error',signal:AbortSignal.timeout(15000)});
 const raw=await response.text();return {status:response.status,body:raw?JSON.parse(raw):{},headers:response.headers};
}
const gateway='http://127.0.0.1:8082',admin='http://127.0.0.1:8090',provider='http://127.0.0.1:18081';
function adminRequest(path,options={}){return request(admin,path,{...options,headers:{Cookie:operator.cookie,Origin:origin,'X-CSRF-Token':operator.csrf??'',...options.headers}});}
function gatewayRequest(path,options={}){return request(gateway,path,{...options,headers:{...(participant?{Authorization:`Bearer ${participant.tokens.access_token}`} : {}),...options.headers}});}
async function upload(kind){
 const grant=await gatewayRequest('/v1/media/uploads',{method:'POST',body:{purpose:'IDENTITY_DOCUMENT',content_type:'image/png',size_bytes:picture.length,sha256:createHash('sha256').update(picture).digest('hex')}});
 check(`${kind}-private-upload-granted`,grant.status===201);
 const url=new URL(grant.body.upload_url);check(`${kind}-local-object-store-only`,url.origin==='http://127.0.0.1:9000');
 const put=await fetch(url,{method:'PUT',headers:grant.body.headers,body:picture,redirect:'error',signal:AbortSignal.timeout(15000)});
 check(`${kind}-private-object-written`,put.status===200);
 const ready=await gatewayRequest(`/v1/media/${grant.body.asset.id}/complete`,{method:'POST'});
 check(`${kind}-private-scan-ready`,ready.status===200&&ready.body.state==='READY');
 return {kind,asset_id:grant.body.asset.id};
}
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
 const context=docker(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(context.status!==0)throw Error('Local Docker unavailable');requireLocalEngine(context.stdout.trim());
 const providerOwner=docker(['inspect','--format','{{index .Config.Labels "com.docker.compose.project"}} {{index .Config.Labels "com.docker.compose.service"}}','planext4u-phase0-synthetic-provider-1']);
 check('isolated-local-synthetic-provider',providerOwner.status===0&&providerOwner.stdout.trim()==='planext4u-phase0 synthetic-provider');
 const transaction=docker(['inspect','--format','{{index .Config.Labels "com.docker.compose.project"}} {{index .Config.Labels "com.docker.compose.service"}} {{range .Config.Env}}{{println .}}{{end}}','planext4u-phase0-transaction-1']);
 check('opt-in-transaction-uses-local-providers',transaction.status===0&&transaction.stdout.includes('planext4u-phase0 transaction')&&transaction.stdout.includes('APP_ENV=development')&&transaction.stdout.includes('KYC_VERIFICATION_BASE_URL=http://synthetic-provider:8081')&&transaction.stdout.includes('OCR_VERIFICATION_BASE_URL=http://synthetic-provider:8081'));
 mkdirSync(resolve(repositoryRoot,'.local/completion/kyc-attestation-journey'),{recursive:true});
 output=mkdtempSync(resolve(repositoryRoot,'.local/completion/kyc-attestation-journey/run-'));
 const subject=randomUUID(), source=randomUUID(), providerSubject=`synthetic-kyc-${run}`;
 operator={subject,source,providerSubject};
 sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${subject}','${providerSubject}','Synthetic KYC reviewer',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']);`);
 const exchange=JSON.stringify({subject_id:subject,session_id:source,tenant_id:tenant,display_name:'Synthetic KYC reviewer',roles:['COUNTRY_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE),signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${exchange}`).digest('hex');
 const session=await fetch(admin+'/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body:exchange,signal:AbortSignal.timeout(10000)});
 check('fresh-synthetic-admin-mfa-session',session.status===201);operator.cookie=session.headers.get('set-cookie')?.split(';')[0];if(!operator.cookie)throw Error('Admin cookie missing');
 sql(`UPDATE admin.sessions SET operator_revision=1 WHERE tenant_id='${tenant}' AND subject_id='${subject}' AND session_id='${source}';`);
 const state=await adminRequest('/admin/api/v1/session');check('country-admin-session-current',state.status===200);operator.csrf=state.body.csrf_token;
 for(const name of ['retention-proposals','deletions','holds']){
  const page=await adminRequest(`/admin/api/v1/privacy/${name}`);
  check(`country-scoped-privacy-${name}-read`,page.status===200&&Array.isArray(page.body.items),{status:page.status,error_code:page.body.error?.code});
 }
 const enrolled=await request(gateway,'/v1/auth/enroll',{method:'POST',body:{provider:'local',provider_token:`synthetic-participant-${randomUUID()}`,device_id:`kyc-${run}`,country:'IN',enroll_as:'VENDOR'}});
 check('normal-synthetic-vendor-enrolled',enrolled.status===201&&enrolled.body.roles.includes('VENDOR'));participant=enrolled.body;
 let app=await gatewayRequest('/v1/vendor/applications',{method:'POST',headers:{'Idempotency-Key':randomUUID()},body:{business_name:`Synthetic KYC ${run}`,business_type:'SOLE_PROPRIETOR',contact_name:'Synthetic local participant'}});
 check('normal-vendor-registered',app.status===201&&app.body.status==='REGISTERED');
 const documents=[await upload('IDENTITY_PROOF'),await upload('BUSINESS_LICENSE')];
 app=await gatewayRequest('/v1/vendor/application/documents',{method:'POST',headers:{'Idempotency-Key':randomUUID(),'If-Match':'1'},body:{documents}});
 check('normal-private-documents-submitted',app.status===200&&app.body.status==='DOCUMENTS_SUBMITTED');
 const reviewPath=`/admin/api/v1/participants/VENDOR/${participant.identity_id}/review-action`;
 const act=(action,revision,key)=>adminRequest(reviewPath,{method:'POST',headers:{'Idempotency-Key':key,'If-Match':String(revision)},body:{action,reason:'Synthetic independent KYC boundary check'}});
 app=await act('START_OCR_REVIEW',2,randomUUID());check('country-admin-starts-review',app.status===200&&app.body.status==='OCR_REVIEW'&&app.body.revision===3);
 const ocrScope={tenant_id:tenant,country:'IN',owner_id:participant.identity_id,document_revision:3,purpose:'VENDOR_ONBOARDING_OCR',documents};
 const scope={tenant_id:tenant,country:'IN',owner_id:participant.identity_id,purpose:'ONBOARDING_KYC',documents};
 const unresolved=await act('VERIFY_KYC',3,randomUUID());check('unissued-ocr-denied-without-mutation',unresolved.status===403,{status:unresolved.status,error_code:unresolved.body.error?.code});
 const afterMissing=await adminRequest(`/admin/api/v1/participants/VENDOR/${participant.identity_id}/review`);check('unissued-proof-keeps-review-revision',afterMissing.status===200&&afterMissing.body.revision===3&&afterMissing.body.status==='OCR_REVIEW');
 const issueOCR=(body)=>request(provider,'/synthetic/documents/ocr-attestations',{method:'POST',headers:{Authorization:`Bearer ${ocrToken}`},body});
 const issue=(body)=>request(provider,'/synthetic/identity-documents/attestations',{method:'POST',headers:{Authorization:`Bearer ${kycToken}`},body});
 const unauthorized=await request(provider,'/v1/identity-documents/verification:resolve',{method:'POST',body:scope});check('provider-requires-credential',unauthorized.status===401);
 const foreignOCR=await issueOCR({...ocrScope,document_revision:2,status:'VERIFIED'});check('stale-revision-ocr-issued-only-for-stale-revision',foreignOCR.status===201);
 const staleOCR=await act('VERIFY_KYC',3,randomUUID());check('stale-revision-ocr-cannot-verify-vendor',staleOCR.status===403);
 const rejectedOCR=await issueOCR({...ocrScope,status:'REJECTED'});check('synthetic-rejected-ocr-recorded',rejectedOCR.status===201);
 const deniedOCR=await act('VERIFY_KYC',3,randomUUID());check('rejected-ocr-denied-without-mutation',deniedOCR.status===403);
 const verifiedOCR=await issueOCR({...ocrScope,status:'VERIFIED'});check('synthetic-matching-ocr-recorded',verifiedOCR.status===201);
 const missingKYC=await act('VERIFY_KYC',3,randomUUID());check('unissued-kyc-denied-after-ocr-verification',missingKYC.status===403);
 const foreign=await issue({...scope,owner_id:randomUUID(),status:'VERIFIED'});check('foreign-owner-attestation-issued-only-for-foreign-owner',foreign.status===201);
 const crossScope=await act('VERIFY_KYC',3,randomUUID());check('foreign-owner-proof-cannot-verify-vendor',crossScope.status===403);
 const rejected=await issue({...scope,status:'REJECTED'});check('synthetic-rejected-attestation-recorded',rejected.status===201);
 const denied=await act('VERIFY_KYC',3,randomUUID());check('rejected-proof-denied-without-mutation',denied.status===403);
 const verified=await issue({...scope,status:'VERIFIED'});check('synthetic-matching-attestation-recorded',verified.status===201);
 const key=randomUUID();const accepted=await act('VERIFY_KYC',3,key);check('matching-proof-allows-current-review',accepted.status===200&&accepted.body.status==='KYC_REVIEW'&&accepted.body.revision===4,{status:accepted.status,error_code:accepted.body.error?.code});
 const replay=await act('VERIFY_KYC',3,key);check('review-retry-replays-without-duplicate-transition',replay.status===200&&replay.headers.get('idempotency-replayed')==='true'&&replay.body.revision===4);
 const final=await adminRequest(`/admin/api/v1/participants/VENDOR/${participant.identity_id}/review`);check('owning-readback-shows-attested-review',final.status===200&&final.body.status==='KYC_REVIEW'&&final.body.revision===4);
}catch(error){failure=error.message;}
finally{
 if(participant){try{await request(gateway,'/v1/auth/revoke',{method:'POST',body:{refresh_token:participant.tokens.refresh_token}});}catch{failure??='Participant session cleanup failed';}}
 if(operator){try{sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${tenant}' AND subject_id='${operator.subject}'; DELETE FROM admin.operator_accounts WHERE tenant_id='${tenant}' AND subject_id='${operator.subject}' AND provider_subject='${operator.providerSubject}'; COMMIT;`);}catch{failure??='Operator session cleanup failed';}}
 if(output){const report={check_id:'local-synthetic-ocr-kyc-attestation-boundary',result:failure?'FAILED':'PASSED',checked_at:new Date().toISOString(),checks,note:'Synthetic local-only OCR/KYC providers, private bytes and scanned documents. This does not prove real OCR/KYC providers, native Android, or production approval.',...(failure?{failure}:{})};writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,checks:checks.length,evidence:resolve(output,'report.json'),...(failure?{failure}:{})}));}else if(failure)console.error(failure);
}
process.exitCode=failure?1:0;
