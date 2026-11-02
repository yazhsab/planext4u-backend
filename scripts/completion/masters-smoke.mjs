// Persistent local acceptance on uniquely owned synthetic tenant/operator rows.
// Financial/business fixtures and provider credentials are never reset.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../admin-web/node_modules/@axe-core/playwright/dist/index.mjs';
import {createHash, createHmac, randomUUID} from 'node:crypto';
import {mkdirSync, mkdtempSync, readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {stackDirectory} from './stack.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {requireLocalEngine} from './verify.mjs';

const origin='https://localhost:4173', tenant=randomUUID(), otherTenant=randomUUID();
const actors=[], checks=[]; let output,report,browser,lastPage,failure;
const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
const check=(id,ok)=>{checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Master data acceptance failed: ${id}`);};
function sql(source){const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});if(result.status!==0)throw new Error('Owned local master fixture operation failed.');}
const uuid=value=>/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);
function manifest(){const inventory=spawnSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(inventory.status!==0)throw new Error('Source inventory unavailable.');return [...new Set(inventory.stdout.split('\0').filter(path=>/^(?:cmd\/admin\/|internal\/(?:adminshell|organization)\/|migrations\/admin\/|admin-web\/src\/|admin-web\/(?:package\.json|pnpm-lock\.yaml|vite\.config\.ts|vitest\.config\.ts)$|api\/openapi\/admin\.openapi\.json$|scripts\/completion\/masters-smoke\.mjs$)/.test(path)))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';}
async function actor(country,role,scope=tenant){
 const subject=randomUUID(),session=randomUUID(),provider=`phase2-master-qa-${subject}`;
 if(!uuid(scope))throw new Error('Invalid generated scope');
 sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${scope}','${subject}','${provider}','Synthetic master acceptance',ARRAY['${role}'],ARRAY['${country}']);`);
 const result={subject,session,provider,tenant:scope,country};actors.push(result);
 const secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
 const body=JSON.stringify({subject_id:subject,session_id:session,tenant_id:scope,display_name:'Synthetic master acceptance',roles:[role],allowed_countries:[country],selected_country:country,authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const exchange=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',body,headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},signal:AbortSignal.timeout(10000)});
 if(exchange.status!==201)throw new Error('Synthetic local MFA exchange failed.');result.cookie=exchange.headers.get('set-cookie')?.split(';')[0];if(!result.cookie)throw new Error('Synthetic secure session unavailable.');
 // The signed development exchange predates directory-bound sessions. Attach
 // ONLY this run's exact generated session to its own current directory row.
 sql(`DO $$ BEGIN UPDATE admin.sessions s SET operator_revision=a.revision FROM admin.operator_accounts a WHERE s.session_id='${session}' AND s.tenant_id='${scope}' AND s.subject_id='${subject}' AND a.tenant_id=s.tenant_id AND a.subject_id=s.subject_id AND a.active; IF NOT FOUND THEN RAISE EXCEPTION 'Owned master session unavailable'; END IF; END $$;`);
 const current=await request(result,'/admin/api/v1/session');if(current.status!==200)throw new Error('Synthetic current session unavailable.');result.csrf=current.body.csrf_token;return result;
}
async function request(who,path,method='GET',body,headers={}){const result=await fetch('http://127.0.0.1:8090'+path,{method,headers:{Cookie:who.cookie,Origin:origin,'Content-Type':'application/json','X-CSRF-Token':who.csrf??'',...headers},...(body===undefined?{}:{body:JSON.stringify(body)}),signal:AbortSignal.timeout(10000)});return {status:result.status,body:await result.json()};}
const occupation=(name='Teacher')=>({id:randomUUID(),kind:'OCCUPATION',code:'TEACHER',name_en:name,name_ta:'ஆசிரியர்',active:true,revision:0,reason:'Synthetic local master acceptance'});
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
 const context=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(context.status!==0)throw new Error('Local Docker context unavailable.');requireLocalEngine(context.stdout.trim());
 mkdirSync(resolve(repositoryRoot,'.local/completion/master-verification'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/master-verification/run-'));
 const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);report={check_id:'local-master-data-administration',started_at:new Date().toISOString(),result:'RUNNING',checks,source_manifest_sha256:createHash('sha256').update(before).digest('hex'),note:'Actual local BFF/PostgreSQL and HTTPS browser. Separate uniquely generated synthetic tenants/operators; signed local MFA plus exact owned directory/session binding. No real Firebase MFA, migration of legacy data, consumer integration or phase-exit proof is inferred.'};
 const root=await actor('IN','COUNTRY_ADMIN'), foreignCountry=await actor('US','COUNTRY_ADMIN'), foreignTenant=await actor('IN','COUNTRY_ADMIN',otherTenant), regional=await actor('IN','REGIONAL_STAFF'), auditor=await actor('IN','AUDITOR');
 const own=occupation();check('own-country-create',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',own)).status===200);
 for(const [name,who] of [['other-country',foreignCountry],['other-tenant',foreignTenant]]){const item=occupation();check(`${name}-same-code-create`,(await request(who,`/admin/api/v1/masters/${item.id}`,'PUT',item)).status===200);const page=await request(who,'/admin/api/v1/masters?kind=OCCUPATION');check(`${name}-list-isolated`,page.status===200&&page.body.items.length===1&&page.body.items[0].id===item.id);check(`${name}-history-denied`,(await request(who,`/admin/api/v1/masters/${own.id}/history`)).status===404);}
 check('regional-direct-read-denied',(await request(regional,'/admin/api/v1/masters?kind=OCCUPATION')).status===403);
 check('regional-direct-command-denied',(await request(regional,`/admin/api/v1/masters/${own.id}`,'PUT',own)).status===403);
 check('auditor-read-allowed',(await request(auditor,'/admin/api/v1/masters?kind=OCCUPATION')).status===200);
 check('auditor-command-denied',(await request(auditor,`/admin/api/v1/masters/${own.id}`,'PUT',own)).status===403);
 check('client-country-injection-denied',(await request(root,'/admin/api/v1/masters?kind=OCCUPATION&country=US')).status===422);
 check('client-authority-injection-denied',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',{...own,tenant_id:otherTenant})).status===422);
 check('csrf-bypass-denied',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',own,{'X-CSRF-Token':'invalid'})).status===403);
 check('literal-wildcard-search',(await request(root,'/admin/api/v1/masters?kind=OCCUPATION&search=%25')).body.items.length===0);
 // Every supported kind is committed through the same normal command path.
 let parent;
 for(const [kind,code] of [['COUNTRY','IN'],['STATE','TN'],['DISTRICT','CHENNAI'],['CITY','CHENNAI'],['AREA','CENTRAL']]){
  const input={...occupation(),kind,code,...(parent?{parent_id:parent.id}:{})};const created=await request(root,`/admin/api/v1/masters/${input.id}`,'PUT',input);check(`create-hierarchy-${kind}`,created.status===200);parent=created.body;
 }
 const badParent={...occupation(),kind:'CITY',code:'INVALID_PARENT',parent_id:parent.id};check('wrong-parent-kind-denied',(await request(root,`/admin/api/v1/masters/${badParent.id}`,'PUT',badParent)).status===422);
 const state=(await request(root,'/admin/api/v1/masters?kind=STATE')).body.items[0];check('active-child-blocks-parent-deactivation',(await request(root,`/admin/api/v1/masters/${state.id}`,'PUT',{...state,active:false,reason:'Synthetic attempt with active child'})).status===409);
 for(let index=0;index<52;index++){
  const item={...occupation(),kind:'REASON',code:`QA_REASON_${index}`};const saved=await request(root,`/admin/api/v1/masters/${item.id}`,'PUT',item);if(saved.status!==200)throw new Error('Owned pagination fixture create failed.');
 }
 const first=await request(root,'/admin/api/v1/masters?kind=REASON&search=QA_REASON_');check('keyset-first-page-bounded',first.status===200&&first.body.items.length===50&&uuid(first.body.next_after));
 const second=await request(root,`/admin/api/v1/masters?kind=REASON&search=QA_REASON_&after=${first.body.next_after}`);check('keyset-second-page-complete',second.status===200&&second.body.items.length===2&&!second.body.next_after&&!second.body.items.some(item=>first.body.items.some(previous=>previous.id===item.id)));
 browser=await chromium.launch({headless:true});
 for(const viewport of [{width:1366,height:900},{width:390,height:844}]){
  const browserContext=await browser.newContext({ignoreHTTPSErrors:true,viewport});const split=root.cookie.indexOf('=');await browserContext.addCookies([{name:root.cookie.slice(0,split),value:root.cookie.slice(split+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);
  const page=await browserContext.newPage();lastPage=page;await page.goto(origin+'/masters');await page.getByRole('heading',{name:'Master data',exact:true}).waitFor();
  await page.getByRole('button',{name:viewport.width===1366?'Edit entry: Teacher':'Edit entry: Updated teacher',exact:true}).click();await page.getByLabel('English name',{exact:true}).fill('Updated teacher');await page.getByLabel('Reason for change').fill(`Synthetic browser acceptance ${viewport.width}`);
  const saved=page.waitForResponse(response=>new URL(response.url()).pathname===`/admin/api/v1/masters/${own.id}`&&response.request().method()==='PUT');await page.getByRole('button',{name:'Save master data',exact:true}).click();const savedResponse=await saved;check(`browser-edit-persists-${viewport.width}`,savedResponse.status()===200);
  await page.getByText('Master data saved with its change history.',{exact:true}).waitFor();await page.getByRole('button',{name:'Change history: Updated teacher',exact:true}).click();await page.getByText(`Synthetic browser acceptance ${viewport.width}`,{exact:true}).waitFor();check(`browser-history-${viewport.width}`,true);
  const accessibility=await new AxeBuilder({page}).analyze();check(`accessibility-no-serious-critical-${viewport.width}`,!accessibility.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
  check(`no-horizontal-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
  await page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo({top:0,behavior:'instant'});});await page.evaluate(()=>new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done))));
  await page.screenshot({path:resolve(output,`masters-${viewport.width}.png`),fullPage:true});
  if(viewport.width===1366){
   await page.getByRole('button',{name:'Close history',exact:true}).click();await page.getByLabel('Master type').selectOption('STATE');await page.getByRole('button',{name:'Edit entry: Teacher',exact:true}).click();
   check('browser-geography-parent-immutable',await page.getByLabel('Parent entry Country',{exact:true}).getAttribute('readonly')!==null);
   await page.getByLabel('English name',{exact:true}).fill('Tamil Nadu');await page.getByLabel('Tamil name',{exact:true}).fill('தமிழ்நாடு');await page.getByLabel('Reason for change').fill('Synthetic geography browser acceptance');
   const changedState=page.waitForResponse(response=>new URL(response.url()).pathname===`/admin/api/v1/masters/${state.id}`&&response.request().method()==='PUT');await page.getByRole('button',{name:'Save master data',exact:true}).click();const stateResponse=await changedState;check('browser-geography-edit-persists',stateResponse.status()===200&&(await stateResponse.json()).name_ta==='தமிழ்நாடு');
  }
  await browserContext.close();
 }
 const history=await request(root,`/admin/api/v1/masters/${own.id}/history`);check('committed-history-revisions',history.status===200&&history.body.items.length===3&&history.body.items[0].definition.revision===3);
 check('stale-edit-does-not-write',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',{...own,revision:1})).status===409);
 check('stable-code-change-denied',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',{...own,revision:3,code:'REASSIGNED'})).status===409);
 check('occupation-deactivation-retains-history',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',{...own,revision:3,active:false})).status===200);
 const inactive=await request(root,'/admin/api/v1/masters?kind=OCCUPATION&status=INACTIVE');check('inactive-filter-readback',inactive.status===200&&inactive.body.items.length===1&&inactive.body.items[0].id===own.id&&inactive.body.items[0].revision===4);
 sql(`UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}' AND subject_id='${root.subject}' AND provider_subject='${root.provider}';`);
 check('open-session-revocation-read-denied',(await request(root,'/admin/api/v1/masters?kind=OCCUPATION')).status===403);
 check('open-session-revocation-command-denied',(await request(root,`/admin/api/v1/masters/${own.id}`,'PUT',{...own,revision:3})).status===403);
 report.source_unchanged=manifest()===before;check('source-unchanged',report.source_unchanged);
}catch(error){failure=error.message;if(lastPage&&!lastPage.isClosed()&&output){try{await lastPage.screenshot({path:resolve(output,'failure.private.png'),fullPage:true});writeFileSync(resolve(output,'failure.private.txt'),(await lastPage.locator('body').innerText()).slice(0,8000));}catch{/* Preserve the original failure. */}}}
finally{
 await browser?.close();
 for(const who of actors){try{sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${who.tenant}' AND subject_id='${who.subject}' AND session_id='${who.session}'; DELETE FROM admin.operator_accounts WHERE tenant_id='${who.tenant}' AND subject_id='${who.subject}' AND provider_subject='${who.provider}'; COMMIT;`);}catch{failure??='Owned master fixture cleanup failed.';}}
 if(output){report??={checks};report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,checks:checks.length,evidence:resolve(output,'report.json'),...(failure?{failure}:{})},null,2));}
 else if(failure)console.error(failure);
}
process.exitCode=failure?1:0;
