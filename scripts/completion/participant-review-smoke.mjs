// Normal local admin/participant acceptance. Every identity, document image,
// provider result and MFA assertion is a uniquely owned synthetic fixture.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../admin-web/node_modules/@axe-core/playwright/dist/index.mjs';
import {createHash,createHmac,randomUUID} from 'node:crypto';
import {existsSync,mkdirSync,mkdtempSync,readFileSync,writeFileSync,unlinkSync,openSync,closeSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';
import {requireLocalEngine} from './verify.mjs';
const run=randomUUID(),checks=[],operators=[],participants=[],mobile=resolve(repositoryRoot,'../planext4u-moble'),origin='https://localhost:4173';
let output,report,browser,privateFile,failure;
const tenant=loadLocalProfile('identity').TENANT_ID;
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw Error(`Participant acceptance failed: ${id}`);}
function command(args,options={}){return spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});}
function sql(input){const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input});if(result.status!==0)throw Error('Owned participant operator fixture unavailable');}
async function request(path,{method='GET',body,token,operator,headers={},owner=false}={}){
 const response=await fetch(`http://127.0.0.1:${owner?'8086':operator?'8090':'8082'}${path}`,{method,headers:{'Content-Type':'application/json',...(token?{Authorization:`Bearer ${token}`} : {}),...(operator?{Cookie:operator.cookie,Origin:origin,'X-CSRF-Token':operator.csrf??''}:{}),...headers},...(body===undefined?{}:{body:JSON.stringify(body)}),signal:AbortSignal.timeout(15000),redirect:'error'});
 const text=await response.text();return {status:response.status,body:text?JSON.parse(text):{}};
}
async function operator(role){
 const subject=randomUUID(),source=randomUUID(),provider=`review-acceptance-${run}-${subject}`;
 sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${subject}','${provider}','Synthetic participant reviewer',ARRAY['${role}'],ARRAY['IN']);`);
 const value={subject,source,provider};operators.push(value);
 const secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE),body=JSON.stringify({subject_id:subject,session_id:source,tenant_id:tenant,display_name:'Synthetic participant reviewer',roles:[role],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const response=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body,signal:AbortSignal.timeout(10000)});
 check(`${role}-current-synthetic-source-session`,response.status===201);value.cookie=response.headers.get('set-cookie')?.split(';')[0];if(!value.cookie)throw Error('Secure source cookie unavailable');
 sql(`UPDATE admin.sessions SET operator_revision=1 WHERE tenant_id='${tenant}' AND subject_id='${subject}' AND session_id='${source}';`);
 const session=await request('/admin/api/v1/session',{operator:value});check(`${role}-directory-session-resolves`,session.status===200);value.csrf=session.body.csrf_token;return value;
}
async function enroll(kind){
 const response=await request('/v1/auth/enroll',{method:'POST',body:{provider:'local',provider_token:`synthetic-participant-${randomUUID()}`,device_id:`review-${run}-${kind}`,country:'IN',enroll_as:kind}});
 check(`${kind}-normal-enrollment`,response.status===201&&response.body.roles.includes(kind));const value=response.body;participants.push(value);return value;
}
async function document(person,label){
 const picture=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII=','base64'),token=person.tokens.access_token;
 const response=await request('/v1/media/uploads',{method:'POST',token,body:{purpose:'IDENTITY_DOCUMENT',content_type:'image/png',size_bytes:picture.length,sha256:createHash('sha256').update(picture).digest('hex')}});check(`${label}-private-upload-grant`,response.status===201);
 const grant=response.body,target=new URL(grant.upload_url);if(target.origin!=='http://127.0.0.1:9000')throw Error('Unexpected local document destination');
 const put=await fetch(target,{method:'PUT',headers:grant.headers,body:picture,redirect:'error',signal:AbortSignal.timeout(15000)});check(`${label}-actual-private-bytes`,put.status===200);
 const ready=await request(`/v1/media/${grant.asset.id}/complete`,{method:'POST',token});check(`${label}-actual-scanner-ready`,ready.status===200&&ready.body.state==='READY');return grant.asset.id;
}
function manifest(){
 const list=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(list.status!==0)throw Error('Source inventory unavailable');
 return [...new Set(list.stdout.split('\0').filter(p=>/^(?:cmd\/|internal\/|migrations\/|api\/openapi\/|admin-web\/src\/|scripts\/completion\/(?:participant-review-smoke\.mjs|fixtures\/participant-review-client\.dart)$)/.test(p)))].sort().map(p=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,p))).digest('hex')}  ${p}`).join('\n')+'\n';
}
try{
 if(!/^[0-9a-f-]{36}$/.test(tenant))throw Error('Invalid local tenant');if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);const context=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(context.status!==0)throw Error('Local Docker unavailable');requireLocalEngine(context.stdout.trim());
 mkdirSync(resolve(repositoryRoot,'.local/completion/participant-review-journey'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/participant-review-journey/run-'));const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);report={check_id:'normal-participant-review-journey',result:'RUNNING',started_at:new Date().toISOString(),checks,note:'Real local enrolment, private S3/ClamAV documents, canonical HTTPS admin rejection/retry and normal Flutter owning readback. Synthetic identities/MFA/documents and local bank reference; no external KYC/bank approval, native device or production-readiness claim. Vendor queue review state is prepared through the actual owning transition with the current synthetic operator source; approval is never exercised.'};
 const admin=await operator('COUNTRY_ADMIN'),regional=await operator('REGIONAL_STAFF'),vendor=await enroll('VENDOR'),rider=await enroll('RIDER');
 check('regional-country-queue-denied',(await request('/admin/api/v1/participants/VENDOR',{operator:regional})).status===403);
 check('caller-country-filter-denied',(await request('/admin/api/v1/participants/VENDOR?country=US',{operator:admin})).status===422);
 check('participant-country-queue-denied',(await request('/v1/vendor/applications',{token:vendor.tokens.access_token})).status===403);
 const vendorName=`Review Vendor ${run}`,riderName=`Review Rider ${run}`;
 let app=await request('/v1/vendor/applications',{method:'POST',token:vendor.tokens.access_token,headers:{'Idempotency-Key':randomUUID()},body:{business_name:vendorName,business_type:'SOLE_PROPRIETOR',contact_name:'Synthetic local participant'}});check('normal-vendor-registration',app.status===201&&app.body.status==='REGISTERED');
 const docs=[];for(const [index,kind] of ['IDENTITY_PROOF','BUSINESS_LICENSE'].entries())docs.push({kind,asset_id:await document(vendor,`vendor-document-${index+1}`)});
 app=await request('/v1/vendor/application/documents',{method:'POST',token:vendor.tokens.access_token,headers:{'Idempotency-Key':randomUUID(),'If-Match':'1'},body:{documents:docs}});check('normal-vendor-documents-submit',app.status===200&&app.body.status==='DOCUMENTS_SUBMITTED');
 const owningHeaders={'X-Planext4u-Tenant':tenant,'X-Planext4u-Country':'IN','X-Planext4u-Subject':admin.subject,'X-Planext4u-Roles':'OPS_ADMIN','X-Planext4u-MFA':'verified','X-Planext4u-Operator-Session':admin.source,'X-Planext4u-Operator-Revision':'1','Idempotency-Key':randomUUID(),'If-Match':'2'};
 app=await request(`/v1/vendor/applications/${vendor.identity_id}/transitions`,{owner:true,method:'POST',headers:owningHeaders,body:{status:'OCR_REVIEW',reason:'Prepare synthetic pending-review application without approval'}});check('owning-current-review-queue-state',app.status===200&&app.body.status==='OCR_REVIEW');
 const riderAsset=await document(rider,'rider-document');const registered=await request('/v1/rider/applications',{method:'POST',token:rider.tokens.access_token,headers:{'Idempotency-Key':randomUUID()},body:{full_name:riderName,vehicle_type:'BICYCLE',vehicle_number:'CYC1',documents:[{kind:'IDENTITY',asset_id:riderAsset}],bank_reference:`bankref_synthetic_${run.replaceAll('-','')}`,zones:['600001']}});check('normal-rider-registration-awaiting-verification',registered.status===201&&registered.body.status==='KYC_REVIEW'&&registered.body.bank_status==='PENDING_VERIFICATION');
 for(const kind of ['VENDOR','RIDER']){const page=await request(`/admin/api/v1/participants/${kind}?search=${encodeURIComponent(run)}`,{operator:admin});check(`${kind}-minimal-current-owning-queue`,page.status===200&&page.body.items.length===1&&page.body.items[0].kind===kind&&!/asset_id|bank_reference|extracted_fields|phone_masked/.test(JSON.stringify(page.body)));}
 browser=await chromium.launch({headless:true});const ctx=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1366,height:900}}),separator=admin.cookie.indexOf('=');await ctx.addCookies([{name:admin.cookie.slice(0,separator),value:admin.cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);const page=await ctx.newPage();await page.goto(origin+'/participants');await page.getByRole('heading',{name:'Participant onboarding',exact:true}).waitFor();
 const reason='Synthetic local review rejection with recorded reason',keys=[];let responses=0;
 await page.route(`**/admin/api/v1/participants/VENDOR/${vendor.identity_id}/reject`,async route=>{keys.push(route.request().headers()['idempotency-key']);if(responses++===0){const result=await route.fetch();check('response-loss-after-actual-owning-commit',result.status()===200);await route.abort('failed');}else await route.continue();});
 async function rejectViaUI(kind,name){if(kind==='RIDER')await page.getByRole('combobox',{name:'Participant type',exact:true}).selectOption(kind);await page.getByLabel('Search applicant name',{exact:true}).fill(name);await page.getByRole('button',{name:'Find applications',exact:true}).click();await page.getByRole('button',{name:`Review rejection: ${name}`,exact:true}).click();await page.getByLabel('Reason for change',{exact:true}).fill(reason);await page.getByRole('button',{name:'Confirm rejection',exact:true}).click();if(kind==='VENDOR'){await page.getByRole('button',{name:'Retry same decision',exact:true}).click();}await page.getByText(`Decision recorded: ${kind==='VENDOR'?'REJECTED (4)':'SUSPENDED (2)'}`,{exact:true}).waitFor();check(`${kind}-normal-admin-rejection-readback`,true);}
 await rejectViaUI('VENDOR',vendorName);check('lost-response-retry-preserved-original-key',keys.length===2&&keys[0]===keys[1]);await rejectViaUI('RIDER',riderName);
 const accessibility=await new AxeBuilder({page}).analyze();check('review-workspace-accessibility',accessibility.violations.length===0);await page.screenshot({path:resolve(output,'review-desktop.png'),fullPage:true});await page.setViewportSize({width:390,height:844});await page.waitForFunction(()=>document.querySelector('.sidebar')?.getBoundingClientRect().right<=1);check('review-workspace-mobile-accessibility',(await new AxeBuilder({page}).analyze()).violations.length===0);check('review-workspace-mobile-no-horizontal-overflow',await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));const textFits=()=>page.evaluate(()=>Array.from(document.querySelectorAll('main h1,main h2,main p,main small,main strong,main legend,main label')).every(element=>{const range=document.createRange();range.selectNodeContents(element);return Array.from(range.getClientRects()).every(rect=>rect.width===0||rect.left>=-1&&rect.right<=window.innerWidth+1)}));check('review-mobile-text-and-controls-fit',await textFits());await page.screenshot({path:resolve(output,'review-mobile.png'),fullPage:true});await page.evaluate(()=>{document.documentElement.style.fontSize='24px';});check('review-large-root-font-text-fits',await textFits());check('review-large-root-font-no-horizontal-overflow',await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));await page.screenshot({path:resolve(output,'review-mobile-large-text.png'),fullPage:true});await ctx.close();
 const fixture=readFileSync(resolve(repositoryRoot,'scripts/completion/fixtures/participant-review-client.dart')),harness=resolve(mobile,'build/live_participant_review_test.dart');if(existsSync(harness)&&!readFileSync(harness).equals(fixture))throw Error('Owned review fixture differs');writeFileSync(harness,fixture);
 privateFile=resolve(mobile,`build/participant-review-${run}.private.json`);writeFileSync(privateFile,JSON.stringify({vendor_token:vendor.tokens.access_token,rider_token:rider.tokens.access_token}),{mode:0o600});
 const runner=resolve(output,'run-client.ps1');writeFileSync(runner,"$ErrorActionPreference='Stop'\nSet-Location 'C:\\planext4u\\planext4u-moble'\n& 'C:\\tmp\\planext4u-review-tools\\flutter\\bin\\flutter.bat' test --no-pub build/live_participant_review_test.dart --reporter expanded\nexit $LASTEXITCODE\n");const fd=openSync(resolve(output,'normal-flutter-http.log'),'w',0o600);let client;try{client=spawnSync('powershell.exe',['-NoProfile','-NonInteractive','-File',runner],{cwd:mobile,env:{...process.env,PLANEXT_REVIEW_CONFIG:privateFile},stdio:['ignore',fd,fd],windowsHide:true,timeout:180000});}finally{closeSync(fd)}check('normal-flutter-owning-decision-status-reason-readback',client.status===0);
 check('stale-rejection-revision-denied',(await request(`/admin/api/v1/participants/RIDER/${rider.identity_id}/reject`,{operator:admin,method:'POST',headers:{'Idempotency-Key':randomUUID(),'If-Match':'1'},body:{reason}})).status===409);
 check('source-session-logout',(await request('/admin/api/v1/session',{operator:admin,method:'DELETE'})).status===204);
 check('old-admin-cookie-rejected',(await request('/admin/api/v1/participants/VENDOR',{operator:admin})).status===401);
 check('owning-revoked-source-review-denied',(await request('/v1/vendor/applications',{owner:true,headers:owningHeaders})).status===403);
 report.source_unchanged=manifest()===before;check('source-unchanged',report.source_unchanged);
}catch(error){failure=error.message;}
finally{
 await browser?.close();if(privateFile&&existsSync(privateFile))unlinkSync(privateFile);
 for(const person of participants){try{await request('/v1/auth/revoke',{method:'POST',body:{refresh_token:person.tokens.refresh_token}});}catch{failure??='Owned participant session cleanup failed';}}
 for(const value of operators){try{sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${tenant}' AND subject_id='${value.subject}'; DELETE FROM admin.operator_accounts WHERE tenant_id='${tenant}' AND subject_id='${value.subject}' AND provider_subject='${value.provider}'; COMMIT;`);}catch{failure??='Owned operator cleanup failed';}}
 if(output){report??={checks};report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,checks:checks.length,evidence:resolve(output,'report.json'),...(failure?{failure}:{})}));}else if(failure)console.error(failure);
}
process.exitCode=failure?1:0;
