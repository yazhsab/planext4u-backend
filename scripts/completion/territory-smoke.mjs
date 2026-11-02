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
const tenant=loadLocalProfile('identity').TENANT_ID;
if(!/^[a-f0-9-]{36}$/.test(tenant))throw new Error('Invalid local fixture tenant.');
const name=`Synthetic territory ${run.slice(0,8)}`,checks=[],sessions=[],nodes=[],roles=[];
let browser,context,lastPage,output,report,failure,fixtures=false;
const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
function sql(source){const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});if(result.status!==0)throw new Error('Owned territory fixture setup/cleanup failed.');}
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Territory acceptance failed: ${id}`);}
function manifest(){const files=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z','--','internal/organization','internal/fulfillment','internal/adminshell','migrations/admin','migrations/fulfillment','admin-web/src','api/openapi/admin.openapi.json','api/openapi/fulfillment.openapi.json','scripts/completion/territory-smoke.mjs'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(files.status!==0)throw new Error('Source inventory unavailable.');return [...new Set(files.stdout.split('\0').filter(Boolean))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';}
async function session(subject,role){
 const body=JSON.stringify({subject_id:subject,session_id:randomUUID(),tenant_id:tenant,display_name:'Synthetic territory QA',roles:[role],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
 const signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const response=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body,signal:AbortSignal.timeout(10000)});
 check(`${role}-local-mfa-exchange`,response.status===201);
 const cookie=response.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('Session cookie unavailable.');sessions.push(cookie);return cookie;
}
async function cookieContext(cookie,viewport={width:1366,height:900}){const value=await browser.newContext({ignoreHTTPSErrors:true,viewport});const i=cookie.indexOf('=');await value.addCookies([{name:cookie.slice(0,i),value:cookie.slice(i+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);return value;}
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
 const engine=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(engine.status!==0)throw new Error('Local engine unavailable.');requireLocalEngine(engine.stdout.trim());
 mkdirSync(resolve(repositoryRoot,'.local/completion/territory-verification'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/territory-verification/run-'));
 const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);
 report={check_id:'local-territory-administration',result:'RUNNING',started_at:new Date().toISOString(),checks,source_manifest_sha256:createHash('sha256').update(before).digest('hex'),note:'Real HTTPS UI, BFF and PostgreSQL. Unique local root/regional operator and delivery territory fixtures; signed local MFA assertions only. External Firebase/MFA and all-domain delegated access are not claimed.'};
 sql(`BEGIN; INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${root}','territory-root-${run}','Synthetic territory root',ARRAY['SUPER_ADMIN'],ARRAY['IN']),('${tenant}','${regional}','territory-region-${run}','Synthetic territory staff',ARRAY['REGIONAL_STAFF'],ARRAY['IN']);
 INSERT INTO fulfillment.territories(id,tenant_id,country,region_id,franchise_identity_id,name,center_latitude,center_longitude,radius_km,postal_codes) VALUES('${territory}','${tenant}','IN','QA-${run}','${root}','${name}',13.0827,80.2707,1,ARRAY['999998']); COMMIT;`);fixtures=true;
 browser=await chromium.launch({headless:true});const rootCookie=await session(root,'SUPER_ADMIN');context=await cookieContext(rootCookie);
 const current=await (await context.request.get(origin+'/admin/api/v1/session')).json();report.principal_roles=current.roles;const headers={Origin:origin,'X-CSRF-Token':current.csrf_token};
 const nodeResponse=await context.request.post(origin+'/admin/api/v1/organization/nodes',{headers,data:{name:`Local authority ${run.slice(0,8)}`,kind:'TERRITORY',parent_id:''}});
 check('owned-organization-node-created',nodeResponse.status()===201);const node=await nodeResponse.json();nodes.push(node.id);const targets=await context.request.get(origin+'/admin/api/v1/organization/territory-bindings');check('owned-target-listed',targets.status()===200&&(await targets.json()).items.some(item=>item.territory_id===territory));await context.close();context=null;
 let revision=0;
 for(const viewport of [{width:1366,height:900},{width:390,height:844}]){
  context=await cookieContext(rootCookie,viewport);const page=await context.newPage();lastPage=page;await page.goto(origin+'/organization');
  const select=page.getByLabel(`Organization territory for ${name}`,{exact:true});await select.waitFor();await select.selectOption(node.id);
  await page.getByLabel(`Reason for linking ${name}`,{exact:true}).fill(`Local acceptance territory link ${viewport.width}`);
  const form=page.locator('form').filter({has:select});await form.getByRole('button',{name:'Save territory link',exact:true}).click();
  await page.getByText('Territory access link saved.',{exact:true}).waitFor();
  const readback=await context.request.get(origin+'/admin/api/v1/organization/territory-bindings');const saved=(await readback.json()).items.find(item=>item.territory_id===territory);
  check(`persisted-binding-${viewport.width}`,saved?.node_id===node.id&&saved.revision===++revision);
  const denied=await context.request.put(origin+`/admin/api/v1/organization/territory-bindings/${territory}`,{headers:{Origin:origin},data:{node_id:node.id,revision,reason:'Missing CSRF must be rejected'}});
  check(`missing-csrf-denied-${viewport.width}`,denied.status()===403);
  const stale=await context.request.put(origin+`/admin/api/v1/organization/territory-bindings/${territory}`,{headers,data:{node_id:node.id,revision:0,reason:'Stale revision must be rejected'}});
  check(`stale-revision-denied-${viewport.width}`,stale.status()===409);
  check(`no-horizontal-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  const axe=await new AxeBuilder({page}).analyze();check(`no-serious-critical-accessibility-${viewport.width}`,!axe.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
  await page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo({top:0,behavior:'instant'});});
  await page.screenshot({path:resolve(output,`territory-${viewport.width}.png`),fullPage:true});await context.close();context=null;
 }
 const delegated=await session(regional,'REGIONAL_STAFF');context=await cookieContext(delegated);
 const staffSession=await (await context.request.get(origin+'/admin/api/v1/session')).json();
 check('delegated-binding-list-denied',(await context.request.get(origin+'/admin/api/v1/organization/territory-bindings')).status()===403);
 check('delegated-binding-write-denied',(await context.request.put(origin+`/admin/api/v1/organization/territory-bindings/${territory}`,{headers:{Origin:origin,'X-CSRF-Token':staffSession.csrf_token},data:{node_id:node.id,revision,reason:'Staff cannot claim another delivery resource'}})).status()===403);
 check('unassigned-operator-has-no-delivery-resources',(await (await context.request.get(origin+'/admin/api/v1/fulfillment/territories')).json()).items?.length===0);
 const rootContext=await cookieContext(rootCookie);
 try{
  const roleResponse=await rootContext.request.post(origin+'/admin/api/v1/organization/roles',{headers,data:{node_id:node.id,name:`Delivery role ${run.slice(0,8)}`,capabilities:['organization.read','resource.read']}});
  check('normal-delivery-job-role-created',roleResponse.status()===201);const job=await roleResponse.json();roles.push(job.id);
  const assignment={subject_id:regional,node_id:node.id,role_id:job.id,status:'ACTIVE',revision:0};
  const assigned=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers,data:assignment});
  check('normal-operator-assigned-to-delivery-territory',assigned.status()===200);const staff=await assigned.json();
  const visible=await context.request.get(origin+'/admin/api/v1/fulfillment/territories',{headers:{'X-Planext4u-Tenant':randomUUID(),'X-Planext4u-Roles':'SUPER_ADMIN'}});
  check('bff-current-assignment-and-session-scope',visible.status()===200&&JSON.stringify((await visible.json()).items.map(item=>item.id))===JSON.stringify([territory]));
  const regionalView=await context.request.get(origin+`/admin/api/v1/fulfillment/regions/QA-${run}`);const dashboard=await regionalView.json();
  check('bff-owning-region-totals',regionalView.status()===200&&dashboard.territories===1&&!('payouts_pending' in dashboard)&&Array.isArray(dashboard.latest_locations)&&Array.isArray(dashboard.recent_field_check_ins));
  check('bff-foreign-region-denied',(await context.request.get(origin+'/admin/api/v1/fulfillment/regions/UNASSIGNED')).status()===403);
  check('bff-caller-authority-query-rejected',(await context.request.get(origin+'/admin/api/v1/fulfillment/territories?roles=SUPER_ADMIN')).status()===422);
  for(const viewport of [{width:1366,height:900},{width:390,height:844}]){
   await context.close();context=await cookieContext(delegated,viewport);const page=await context.newPage();lastPage=page;await page.goto(origin+'/organization');
   await page.getByRole('heading',{name:'Delivery operations',exact:true}).waitFor();await page.getByText(name+` · QA-${run}`,{exact:true}).waitFor();
   await page.getByText('Online riders',{exact:true}).waitFor();
   check(`normal-delegated-delivery-ui-${viewport.width}`,await page.getByLabel('Delivery region',{exact:true}).inputValue()===`QA-${run}`);
   check(`read-only-operator-has-no-management-controls-${viewport.width}`,await page.getByRole('button',{name:'Create',exact:true}).count()===0&&await page.getByRole('button',{name:'Suspend',exact:true}).count()===0&&await page.getByRole('button',{name:'Create invitation',exact:true}).count()===0);
   check(`delegated-delivery-no-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
   const axe=await new AxeBuilder({page}).analyze();check(`delegated-delivery-accessibility-${viewport.width}`,!axe.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
   await page.screenshot({path:resolve(output,`delegated-delivery-${viewport.width}.png`),fullPage:true});
   if(viewport.width===390){
    await page.addStyleTag({content:':root{font-size:200%}'});
    check('delegated-large-text-no-horizontal-overflow',await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    const largeAxe=await new AxeBuilder({page}).analyze();check('delegated-large-text-accessibility',!largeAxe.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
    await page.screenshot({path:resolve(output,'delegated-delivery-large-text-390.png'),fullPage:true});
   }
  }
  const suspended=await rootContext.request.put(origin+'/admin/api/v1/organization/staff',{headers,data:{...assignment,status:'SUSPENDED',revision:staff.revision}});
  check('normal-delivery-assignment-suspended',suspended.status()===200);
  check('bff-revocation-removes-territories',(await (await context.request.get(origin+'/admin/api/v1/fulfillment/territories')).json()).items.length===0);
  check('bff-revocation-denies-direct-region',(await context.request.get(origin+`/admin/api/v1/fulfillment/regions/QA-${run}`)).status()===403);
  await lastPage.getByRole('button',{name:'Refresh delivery operations',exact:true}).click();
  await lastPage.getByText('No delivery territories are available under your current assignments.',{exact:true}).waitFor();
  check('normal-ui-clears-revoked-delivery-data',await lastPage.getByText(name+` · QA-${run}`,{exact:true}).count()===0&&await lastPage.getByText('Online riders',{exact:true}).count()===0);
 }finally{await rootContext.close();}
 await context.close();context=null;report.source_unchanged=before===manifest();check('source-unchanged',report.source_unchanged);
}catch(error){failure=error.message;if(lastPage&&!lastPage.isClosed()&&output){try{await lastPage.screenshot({path:resolve(output,'failure.png'),fullPage:true});writeFileSync(resolve(output,'failure-ui.txt'),(await lastPage.locator('body').innerText()).slice(0,12000));}catch{}}}
finally{
 await context?.close();
 if(browser){for(const cookie of sessions){try{const cleanup=await cookieContext(cookie);const value=await (await cleanup.request.get(origin+'/admin/api/v1/session')).json();check('owned-session-revoked',(await cleanup.request.delete(origin+'/admin/api/v1/session',{headers:{Origin:origin,'X-CSRF-Token':value.csrf_token}})).status()===204);await cleanup.close();}catch(error){failure??=error.message;}}}
 await browser?.close();
 if(fixtures){try{sql(`DELETE FROM admin.staff_assignments WHERE tenant_id='${tenant}' AND subject_id='${regional}';${roles.map(id=>`DELETE FROM admin.job_roles WHERE tenant_id='${tenant}' AND id='${id}';`).join('')}DELETE FROM admin.fulfillment_territory_bindings WHERE tenant_id='${tenant}' AND territory_id='${territory}';DELETE FROM fulfillment.territories WHERE tenant_id='${tenant}' AND id='${territory}';${nodes.map(id=>`DELETE FROM admin.organization_nodes WHERE tenant_id='${tenant}' AND id='${id}';`).join('')}UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}' AND subject_id IN ('${root}','${regional}');`);check('owned-fixtures-retired-audit-retained',true);}catch(error){failure??=error.message;}}
 if(report){report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
 console.log(JSON.stringify({result:failure?'FAILED':'PASSED',checks:checks.length,evidence:output?resolve(output,'report.json'):null,...(failure?{failure}:{})},null,2));process.exitCode=failure?2:0;
}
