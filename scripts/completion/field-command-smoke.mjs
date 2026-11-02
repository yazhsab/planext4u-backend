// Real local HTTPS BFF/browser verification. Only uniquely named synthetic
// fixtures are created; this never resets business or financial configuration.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../admin-web/node_modules/@axe-core/playwright/dist/index.mjs';
import {createHash,createHmac,randomUUID} from 'node:crypto';
import {mkdirSync,mkdtempSync,readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';
import {requireLocalEngine} from './verify.mjs';

const origin='https://localhost:4173',run=randomUUID(),territory=randomUUID(),root=randomUUID(),regional=randomUUID();
const tenant=randomUUID();
if(!/^[a-f0-9-]{36}$/.test(tenant))throw new Error('Invalid local fixture tenant.');
const name=`Synthetic territory ${run.slice(0,8)}`,checks=[],sessions=[],nodes=[],roles=[];
let browser,context,lastPage,output,report,failure,fixtures=false;
const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
function sql(source){const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});if(result.status!==0)throw new Error('Owned territory fixture setup/cleanup failed.');}
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Territory acceptance failed: ${id}`);}
function manifest(){const files=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z','--','internal/organization','internal/fulfillment','internal/adminshell','migrations/admin','migrations/fulfillment','admin-web/src','api/openapi/admin.openapi.json','api/openapi/fulfillment.openapi.json','scripts/completion/field-command-smoke.mjs'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(files.status!==0)throw new Error('Source inventory unavailable.');return [...new Set(files.stdout.split('\0').filter(Boolean))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';}
async function session(subject,role){
 const sourceSession=randomUUID();
 const body=JSON.stringify({subject_id:subject,session_id:sourceSession,tenant_id:tenant,display_name:'Synthetic territory QA',roles:[role],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
 const signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const response=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body,signal:AbortSignal.timeout(10000)});
 check(`${role}-local-mfa-exchange`,response.status===201);
 const cookie=response.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('Session cookie unavailable.');sessions.push(cookie);
 // The local signed exchange predates directory revisions. Bind only this
 // synthetic test session to its actual fixture account revision; production
 // Firebase sign-in obtains that revision from the directory itself.
 sql(`DO $$ BEGIN UPDATE admin.sessions s SET operator_revision=a.revision FROM admin.operator_accounts a WHERE s.session_id='${sourceSession}' AND s.tenant_id='${tenant}' AND s.subject_id='${subject}' AND a.tenant_id=s.tenant_id AND a.subject_id=s.subject_id AND a.active; IF NOT FOUND THEN RAISE EXCEPTION 'Owned test session revision unavailable'; END IF; END $$;`);
 return cookie;
}
async function cookieContext(cookie,viewport={width:1366,height:900},gps=false){const value=await browser.newContext({ignoreHTTPSErrors:true,viewport,...(gps?{permissions:['geolocation'],geolocation:{latitude:13.0827,longitude:80.2707,accuracy:5}}:{})});const i=cookie.indexOf('=');await value.addCookies([{name:cookie.slice(0,i),value:cookie.slice(i+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);return value;}
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
 const engine=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(engine.status!==0)throw new Error('Local engine unavailable.');requireLocalEngine(engine.stdout.trim());
 mkdirSync(resolve(repositoryRoot,'.local/completion/field-command-verification'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/field-command-verification/run-'));
 const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);
 report={check_id:'local-delegated-field-command',result:'RUNNING',started_at:new Date().toISOString(),checks,source_manifest_sha256:createHash('sha256').update(before).digest('hex'),external_provider_verified:false,device_gps_verified:false,note:'Real local HTTPS browser, BFF, owner and PostgreSQL. Fresh synthetic tenant/operators/territory, signed local MFA, fixture session directory revisions and browser-simulated geolocation. Production Firebase sign-in is separate. An injected lost response tests normal retry after owning commit. Accepted visit/audit records are retained under the retired synthetic tenant; no money/configuration resets.'};
 writeFileSync(resolve(output,'fixture-identities.private.json'),JSON.stringify({tenant,root,regional,territory,run}));
 sql(`BEGIN; INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${root}','field-root-${run}','Synthetic field root',ARRAY['SUPER_ADMIN'],ARRAY['IN']),('${tenant}','${regional}','field-staff-${run}','Synthetic field staff',ARRAY['REGIONAL_STAFF'],ARRAY['IN']); INSERT INTO fulfillment.territories(id,tenant_id,country,region_id,franchise_identity_id,name,center_latitude,center_longitude,radius_km,postal_codes) VALUES('${territory}','${tenant}','IN','QA-${run}','${root}','${name}',13.0827,80.2707,1,ARRAY['999998']); COMMIT;`);fixtures=true;
 browser=await chromium.launch({headless:true});
 const rootCookie=await session(root,'SUPER_ADMIN');const rootContext=await cookieContext(rootCookie);
 try{
  const rootSession=await(await rootContext.request.get(origin+'/admin/api/v1/session')).json();const rootHeaders={Origin:origin,'X-CSRF-Token':rootSession.csrf_token};
  const created=await rootContext.request.post(origin+'/admin/api/v1/organization/nodes',{headers:rootHeaders,data:{name:`Field proof ${run.slice(0,8)}`,kind:'TERRITORY',parent_id:''}});check('normal-organization-node-created',created.status()===201);const node=await created.json();nodes.push(node.id);
  check('normal-business-territory-bound',(await rootContext.request.put(origin+`/admin/api/v1/organization/territory-bindings/${territory}`,{headers:rootHeaders,data:{node_id:node.id,revision:0,reason:'Verified local field visit acceptance'}})).status()===204);
  const readResponse=await rootContext.request.post(origin+'/admin/api/v1/organization/roles',{headers:rootHeaders,data:{node_id:node.id,name:`Field read ${run.slice(0,8)}`,capabilities:['organization.read','resource.read']}});check('normal-read-role-created',readResponse.status()===201);const readRole=await readResponse.json();roles.push(readRole.id);
  let assignment={subject_id:regional,node_id:node.id,role_id:readRole.id,status:'ACTIVE',revision:0};
  let assigned=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:assignment});check('normal-read-only-assignment',assigned.status()===200);let staff=await assigned.json();
  const staffCookie=await session(regional,'REGIONAL_STAFF');context=await cookieContext(staffCookie);
  const staffSession=await(await context.request.get(origin+'/admin/api/v1/session')).json();const staffHeaders={Origin:origin,'X-CSRF-Token':staffSession.csrf_token};
  const point={latitude:13.0827,longitude:80.2707};const endpoint=origin+`/admin/api/v1/fulfillment/territories/${territory}/field-check-ins`;
  check('read-only-field-scope-empty',(await(await context.request.get(origin+'/admin/api/v1/fulfillment/field-territories')).json()).territory_ids.length===0);
  check('read-only-direct-command-denied',(await context.request.post(endpoint,{headers:{...staffHeaders,'Idempotency-Key':randomUUID()},data:{point}})).status()===403);
  const exportEndpoint=origin+`/admin/api/v1/fulfillment/regions/QA-${run}/summary-export`;
  check('read-only-assignment-cannot-export',(await context.request.get(exportEndpoint)).status()===403);
  check('export-authority-query-rejected',(await context.request.get(exportEndpoint+'?country=NG')).status()===422);
  check('global-role-alone-cannot-log-field-attendance',(await rootContext.request.post(endpoint,{headers:{...rootHeaders,'Idempotency-Key':randomUUID()},data:{point}})).status()===403);
  const commandResponse=await rootContext.request.post(origin+'/admin/api/v1/organization/roles',{headers:rootHeaders,data:{node_id:node.id,name:`Field command ${run.slice(0,8)}`,capabilities:['organization.read','resource.read','resource.command']}});check('normal-command-role-created',commandResponse.status()===201);const commandRole=await commandResponse.json();roles.push(commandRole.id);
  const retiredRead=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:{...assignment,status:'SUSPENDED',revision:staff.revision}});check('normal-read-only-assignment-suspended',retiredRead.status()===200);
  // The role is part of an assignment's identity. A new role uses revision 0;
  // the prior assignment is suspended through the normal command above.
  assignment={...assignment,role_id:commandRole.id,revision:0};assigned=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:assignment});check('normal-current-command-assignment',assigned.status()===200);staff=await assigned.json();
  check('command-permission-does-not-grant-export',(await context.request.get(exportEndpoint)).status()===403);
  const exportRoleResponse=await rootContext.request.post(origin+'/admin/api/v1/organization/roles',{headers:rootHeaders,data:{node_id:node.id,name:`Delivery export ${run.slice(0,8)}`,capabilities:['organization.read','resource.read','resource.export']}});check('normal-export-role-created',exportRoleResponse.status()===201);const exportRole=await exportRoleResponse.json();
  const exportAssignmentResponse=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:{subject_id:regional,node_id:node.id,role_id:exportRole.id,status:'ACTIVE',revision:0}});check('normal-export-assignment-created',exportAssignmentResponse.status()===200);const exportAssignment=await exportAssignmentResponse.json();
  const scope=await context.request.get(origin+'/admin/api/v1/fulfillment/field-territories');check('current-command-scope-from-server',scope.status()===200&&JSON.stringify((await scope.json()).territory_ids)===JSON.stringify([territory]));
  check('field-scope-authority-query-denied',(await context.request.get(origin+'/admin/api/v1/fulfillment/field-territories?subject_id='+root)).status()===422);
  check('missing-command-csrf-denied',(await context.request.post(endpoint,{headers:{Origin:origin,'Idempotency-Key':randomUUID()},data:{point}})).status()===403);
  check('caller-officer-authority-denied',(await context.request.post(endpoint,{headers:{...staffHeaders,'Idempotency-Key':randomUUID()},data:{point,officer_id:root}})).status()===422);
  let committed,originalKey,originalBody;
  for(const viewport of [{width:1366,height:900},{width:390,height:844}]){
   await context.close();context=await cookieContext(staffCookie,viewport,true);const page=await context.newPage();lastPage=page;await page.goto(origin+'/organization');
   const capture=page.getByRole('button',{name:'Use current location',exact:true});await capture.waitFor();
   await capture.click();await page.getByText('Approximate accuracy: 5 m',{exact:true}).waitFor();check(`normal-browser-location-capture-${viewport.width}`,true);
   if(viewport.width===1366){
    const pattern='**/admin/api/v1/fulfillment/territories/*/field-check-ins';
    let interceptedStatus,interceptedError,finishIntercept;const intercepted=new Promise(resolve=>{finishIntercept=resolve;});
    await page.route(pattern,async route=>{try{originalKey=route.request().headers()['idempotency-key'];originalBody=route.request().postData();const accepted=await route.fetch();interceptedStatus=accepted.status();if(interceptedStatus!==201){interceptedError=(await accepted.json()).error?.code;await route.fulfill({response:accepted});return;}committed=(await accepted.json()).id;await route.abort('failed');}catch(error){interceptedError=error.message;try{await route.abort('failed');}catch{}}finally{finishIntercept();}});
    await page.getByRole('button',{name:'Confirm location and record visit',exact:true}).click();await intercepted;
    if(interceptedStatus!==201)throw new Error(`Owning field command was not accepted: ${interceptedStatus} ${interceptedError??''}`);
    check('owner-committed-before-injected-response-loss',true);await page.getByText('The visit was not confirmed. Retry the same visit to check its result.',{exact:true}).waitFor();await page.unroute(pattern);
    const response=page.waitForResponse(value=>value.request().method()==='POST'&&value.url()===endpoint);
    await page.getByRole('button',{name:'Retry the same visit',exact:true}).click();const replay=await response;const value=await replay.json();
    check('normal-ui-retries-same-owning-result',replay.status()===201&&replay.headers()['idempotency-replayed']==='true'&&value.id===committed&&replay.request().headers()['idempotency-key']===originalKey&&replay.request().postData()===originalBody&&value.officer_id===regional);
    await page.getByText('Field visit recorded.',{exact:true}).waitFor();
    const dashboard=await(await context.request.get(origin+`/admin/api/v1/fulfillment/regions/QA-${run}`)).json();check('owning-readback-has-one-accepted-visit',dashboard.recent_field_check_ins.length===1&&dashboard.recent_field_check_ins[0].id===committed);
    check('same-reference-different-point-conflicts',(await context.request.post(endpoint,{headers:{...staffHeaders,'Idempotency-Key':originalKey},data:{point:{...point,latitude:13.0828}}})).status()===409);
   }else{
    await page.getByRole('button',{name:'Clear location and start again',exact:true}).click();check('explicit-clear-discards-browser-location',await page.getByRole('button',{name:'Confirm location and record visit',exact:true}).count()===0);
   }
   const exportResponse=page.waitForResponse(value=>value.request().method()==='GET'&&value.url()===exportEndpoint);
   await page.getByRole('button',{name:'Prepare delivery summary (CSV)',exact:true}).click();const prepared=await exportResponse;
   const csv=await prepared.text(),csvRows=csv.trimEnd().split(/\r?\n/),columns=csvRows[1]?.split(',');
   const totals=await (await context.request.get(origin+`/admin/api/v1/fulfillment/regions/QA-${run}`)).json();
   check(`normal-ui-owner-export-${viewport.width}`,prepared.status()===200&&prepared.headers()['cache-control']==='private, no-store'&&csvRows.length===2&&columns?.length===8&&columns[0]==='IN'&&columns[1]===`QA-${run}`&&Number(columns[3])===totals.territories&&Number(columns[4])===totals.online_riders&&Number(columns[5])===totals.unassigned_tasks&&Number(columns[6])===totals.at_risk_tasks&&Number(columns[7])===totals.completed_today&&!/rider_id|latitude|longitude|payout|officer_id/i.test(csvRows[0]));
   const downloading=page.waitForEvent('download');await page.getByRole('link',{name:'Download delivery summary CSV',exact:true}).click();const download=await downloading;const savedPath=resolve(output,`delivery-summary-${viewport.width}.csv`);await download.saveAs(savedPath);
   check(`actual-browser-export-readback-${viewport.width}`,download.suggestedFilename()==='delivery-summary.csv'&&readFileSync(savedPath,'utf8')===csv);
   check(`field-no-horizontal-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
   const axe=await new AxeBuilder({page}).analyze();check(`field-no-serious-critical-accessibility-${viewport.width}`,!axe.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
   await page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo(0,0);});
   await page.screenshot({path:resolve(output,`field-command-${viewport.width}.png`),fullPage:true});
   if(viewport.width===390){await page.addStyleTag({content:':root{font-size:200%}'});check('field-large-root-font-no-overflow',await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));const large=await new AxeBuilder({page}).analyze();check('field-large-root-font-accessibility',!large.violations.some(item=>item.impact==='serious'||item.impact==='critical'));await page.screenshot({path:resolve(output,'field-command-large-text-390.png'),fullPage:true});}
  }
  const exportSuspended=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:{...exportAssignment,status:'SUSPENDED'}});check('normal-export-assignment-suspended',exportSuspended.status()===200);
  check('read-command-remain-without-export-authority',(await context.request.get(exportEndpoint)).status()===403&&(await context.request.get(origin+'/admin/api/v1/fulfillment/territories')).status()===200);
  const suspended=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers:rootHeaders,data:{...assignment,status:'SUSPENDED',revision:staff.revision}});check('normal-command-assignment-suspended',suspended.status()===200);
  check('revocation-removes-command-scope',(await(await context.request.get(origin+'/admin/api/v1/fulfillment/field-territories')).json()).territory_ids.length===0);
  check('revocation-denies-accepted-result-replay',(await context.request.post(endpoint,{headers:{...staffHeaders,'Idempotency-Key':originalKey},data:JSON.parse(originalBody)})).status()===403);
  await lastPage.getByRole('button',{name:'Refresh delivery operations',exact:true}).click();await lastPage.getByText('No delivery territories are available under your current assignments.',{exact:true}).waitFor();check('normal-ui-removes-revoked-field-controls',await lastPage.getByRole('button',{name:'Use current location',exact:true}).count()===0);
  check('final-source-unchanged',before===manifest());report.source_unchanged=before===manifest();
 }finally{await rootContext.close();}
}catch(error){failure=error.message;if(lastPage&&!lastPage.isClosed()&&output){try{await lastPage.screenshot({path:resolve(output,'failure.png'),fullPage:true});writeFileSync(resolve(output,'failure-ui.private.txt'),(await lastPage.locator('body').innerText()).slice(0,12000));}catch{}}}
finally{
 await context?.close();
 if(browser){for(const cookie of sessions){try{const cleanup=await cookieContext(cookie);const value=await(await cleanup.request.get(origin+'/admin/api/v1/session')).json();check('owned-session-revoked',(await cleanup.request.delete(origin+'/admin/api/v1/session',{headers:{Origin:origin,'X-CSRF-Token':value.csrf_token}})).status()===204);await cleanup.close();}catch(error){failure??=error.message;}}}
 await browser?.close();
 if(fixtures){try{sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${tenant}'; UPDATE admin.staff_assignments SET status='SUSPENDED',revision=revision+1 WHERE tenant_id='${tenant}'; UPDATE admin.organization_nodes SET active=false,revision=revision+1 WHERE tenant_id='${tenant}'; UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}'; COMMIT;`);check('synthetic-tenant-retired-accepted-visit-and-audit-retained',true);}catch(error){failure??=error.message;}}
 if(report){report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
}
console.log(JSON.stringify({result:failure?'FAILED':'PASSED',checks:checks.length,evidence:output,...(failure?{failure}:{})},null,2));if(failure)process.exitCode=1;
