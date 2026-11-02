// Normal local CMS draft / independent approval publication. No financial,
// policy or business-transition seeds are changed; credentials stay in memory.
import {createHash,createHmac,randomUUID} from 'node:crypto';
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import https from 'node:https';
import {setTimeout as delay} from 'node:timers/promises';
import {repositoryRoot} from './check.mjs';
import {runtimeDirectory,loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';
import {requireLocalEngine} from './verify.mjs';

const tenant=loadLocalProfile('identity').TENANT_ID,admin='https://localhost:4173';
if(!/^[0-9a-f-]{36}$/.test(tenant)||loadLocalProfile('admin').APP_ENV!=='development')throw new Error('Only the local development profile is supported.');
const run=randomUUID(),maker=randomUUID(),checker=randomUUID(),checks=[],sessions=[];
const output=resolve(repositoryRoot,'.local/completion/published-introductions','run-'+run);
mkdirSync(output,{recursive:true});
const ca=readFileSync(resolve(runtimeDirectory,'localhost.crt'));
let fixtures=false,failure;
const report={check_id:'local-published-mobile-introductions',result:'RUNNING',started_at:new Date().toISOString(),checks,pages:[],note:'Normal same-tenant CMS drafts and independent approvals. New published English role introductions remain available locally. Other configuration is preserved. Operator identities and MFA assertions are synthetic; no remote provider/release proof.'};
function sql(input){const result=spawnSync('docker',['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{cwd:repositoryRoot,input,encoding:'utf8',windowsHide:true,timeout:30000});if(result.status!==0)throw new Error('Owned introduction operator fixture failed.');}
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Introduction publication check failed: ${id}`);}
async function request(url,{method='GET',headers={},body}={}){
 const options={method,headers:{'X-Correlation-ID':randomUUID(),...headers}};
 if(body!==undefined){options.body=JSON.stringify(body);options.headers['Content-Type']='application/json';}
 if(url.startsWith(admin))return new Promise((accept,reject)=>{const req=https.request(url,{method,headers:options.headers,ca,family:4,timeout:10000},response=>{let text='';response.on('data',chunk=>{text+=chunk;});response.on('end',()=>{let data;try{data=JSON.parse(text);}catch{data={};}accept({status:response.statusCode,data,headers:response.headers});});});req.on('error',reject);req.on('timeout',()=>req.destroy(new Error('Local CMS request timed out.')));if(options.body)req.write(options.body);req.end();});
 const response=await fetch(url,{...options,signal:AbortSignal.timeout(10000)});let data;try{data=await response.json();}catch{data={};}return {status:response.status,data,headers:Object.fromEntries(response.headers)};
}
async function session(subject){
 const body={subject_id:subject,session_id:randomUUID(),tenant_id:tenant,display_name:'Synthetic introduction QA',roles:['COUNTRY_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']};
 const timestamp=String(Math.floor(Date.now()/1000)),secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
 const signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${JSON.stringify(body)}`).digest('hex');
 const response=await request('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body});
 check('local-country-operator-mfa-exchange',response.status===201);
 const cookie=response.headers['set-cookie']?.split(';')[0];if(!cookie)throw new Error('Local operator cookie unavailable.');
 const view=await request(admin+'/admin/api/v1/session',{headers:{Cookie:cookie}});check('local-country-session-read',view.status===200);
 const headers={Cookie:cookie,Origin:admin,'X-CSRF-Token':view.data.csrf_token};sessions.push(headers);return headers;
}
const configURL='http://127.0.0.1:8084/v1/bootstrap?platform=android&app_version=0.1.0&locale=en';
const scope={'X-Planext4u-Tenant':tenant,'X-Planext4u-Country':'IN'};
const nonPageHash=value=>{const copy={...value};delete copy.revision;delete copy.published_at;delete copy.pages;return createHash('sha256').update(JSON.stringify(copy)).digest('hex');};
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
 const engine=spawnSync('docker',['context','inspect','--format','{{(index .Endpoints "docker").Host}}'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:10000});
 if(engine.status!==0)throw new Error('Local engine unavailable.');requireLocalEngine(engine.stdout.trim());
 sql(`BEGIN;INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${maker}','intro-maker-${run}','Synthetic introduction maker',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']),('${tenant}','${checker}','intro-checker-${run}','Synthetic introduction approver',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']);COMMIT;`);fixtures=true;
 const makerHeaders=await session(maker),checkerHeaders=await session(checker);
 const before=await request(configURL,{headers:scope});check('existing-mobile-configuration-read',before.status===200);let revision=before.data.revision;
 const preserved=nonPageHash(before.data);
 const drafts=await request(admin+'/admin/api/v1/cms/pages',{headers:makerHeaders});check('normal-cms-drafts-read',drafts.status===200&&Array.isArray(drafts.data.items));
 for(const role of ['CUSTOMER','VENDOR','RIDER']){
  const lower=role.toLowerCase(),id=`mobile-onboarding-${lower}-en`;
  const existing=before.data.pages.find(page=>page.id===id);
  if(existing){check(`${lower}-existing-introduction-preserved`,existing.enabled&&existing.audience.includes(role));report.pages.push({role,id,preserved:true});continue;}
  check(`${lower}-unpublished-draft-preserved`,!drafts.data.items.some(draft=>draft.page.id===id));
  const roleName=lower[0].toUpperCase()+lower.slice(1);
  const page={id,route:`/app/onboarding/${lower}/en`,title_key:`onboarding.${lower}.en`,audience:[role],enabled:true,blocks:[
   {id:'welcome',kind:'ONBOARDING',enabled:true,priority:0,content:{version:'local-2026-10-1',title:`Welcome to Planext4u ${roleName}`,message:role==='CUSTOMER'?'Find products and services available in your area. Your account and approved access determine the services you can use.':role==='VENDOR'?'Complete your business profile and upload private verification documents. Approval and verified bank details are required before protected business actions are available.':'Complete your identity and vehicle verification. Review the current delivery location policy before starting duty.'}},
   {id:'privacy',kind:'ONBOARDING',enabled:true,priority:1,content:{version:'local-2026-10-1',title:'Your account and privacy',message:'Choose when to share device permissions. Review active sessions, withdraw optional consent and manage your account from your profile.'}},
  ]};
  const saved=await request(admin+`/admin/api/v1/cms/pages/${id}/draft`,{method:'PUT',headers:makerHeaders,body:{expected_revision:0,page}});check(`${lower}-normal-draft-saved`,saved.status===200);
  const change=await request(admin+'/admin/api/v1/operations',{method:'POST',headers:makerHeaders,body:{domain:'CMS',action:'PUBLISH',target_id:id,reason:'Configure local mobile introduction through independent approval',payload:{publication_type:'PAGE',expected_draft_revision:saved.data.revision,expected_snapshot_revision:revision}}});check(`${lower}-independent-approval-required`,change.status===201&&change.data.status==='PENDING_APPROVAL');
  const approve=admin+`/admin/api/v1/operations/${change.data.id}/approve`;
  check(`${lower}-maker-self-approval-denied`,(await request(approve,{method:'POST',headers:makerHeaders,body:{expected_revision:change.data.revision}})).status===409);
  const approved=await request(approve,{method:'POST',headers:checkerHeaders,body:{expected_revision:change.data.revision}});check(`${lower}-independent-owner-publication`,approved.status===200&&approved.data.status==='EXECUTED');
  revision++;report.pages.push({role,id,preserved:false,content_sha256:createHash('sha256').update(JSON.stringify(page)).digest('hex')});
 }
 const deadline=Date.now()+65000;let after;
 do{after=await request(configURL,{headers:scope});if(after.status!==200||after.data.revision>=revision)break;await delay(250);}while(Date.now()<deadline);
 check('owning-cache-publication-budget',after.status===200&&after.data.revision>=revision);
 check('non-page-configuration-preserved',nonPageHash(after.data)===preserved);
 for(const {role,id} of report.pages){const value=await request(`http://127.0.0.1:8084/v1/pages/${id}?locale=en`,{headers:{...scope,'X-Planext4u-Roles':role}});check(`${role.toLowerCase()}-published-owning-role-read`,value.status===200&&value.data.page.id===id);check(`${role.toLowerCase()}-foreign-role-denied`,(await request(`http://127.0.0.1:8084/v1/pages/${id}?locale=en`,{headers:{...scope,'X-Planext4u-Roles':role==='CUSTOMER'?'RIDER':'CUSTOMER'}})).status===404);}
 report.snapshot_revision=revision;
}catch(error){failure=error.message;}
finally{
 for(const headers of sessions){try{check('owned-publication-session-revoked',(await request(admin+'/admin/api/v1/session',{method:'DELETE',headers})).status===204);}catch(error){failure??=error.message;}}
 if(fixtures){try{sql(`UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}' AND subject_id IN('${maker}','${checker}');`);check('owned-publication-operators-retired',true);}catch(error){failure??=error.message;}}
 report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;
 writeFileSync(resolve(output,'publication.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,checks:checks.length,pages:report.pages.length,evidence:resolve(output,'publication.json'),...(failure?{failure}:{})},null,2));process.exitCode=failure?2:0;
}
