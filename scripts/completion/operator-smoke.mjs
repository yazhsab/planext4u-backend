// Real local BFF/browser acceptance. Only this run's explicitly named operator
// fixture is bootstrapped; business/financial records are never reset.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../admin-web/node_modules/@axe-core/playwright/dist/index.mjs';
import {createHash, createHmac, randomUUID} from 'node:crypto';
import {mkdirSync, mkdtempSync, readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';
import {requireLocalEngine} from './verify.mjs';

const origin='https://localhost:4173';
const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
const uuid=value=>/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);
const runID=randomUUID(), subjectID=randomUUID();
const providerSubject=`phase2-operators-qa-${runID}`;
const rootProvider=`phase2-operators-root-${runID}`;
const name=`Local operator QA ${runID.slice(0,8)}`;
const checks=[];
let browser,lastPage,tenant,bootstrapped=false,cookie,output,report,failure;
function check(id,passed) {checks.push({id,result:passed?'PASSED':'FAILED'});if(!passed)throw new Error(`Local operator acceptance failed: ${id}`);}
function sql(source) {
  const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});
  if(result.status!==0)throw new Error('Local owned operator fixture setup/cleanup failed.');
}
function manifest() {
  const inventory=spawnSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});
  if(inventory.status!==0)throw new Error('Source inventory unavailable.');
  return [...new Set(inventory.stdout.split('\0').filter(path=>/^(?:cmd\/admin\/|internal\/adminshell\/|migrations\/admin\/|admin-web\/src\/|admin-web\/(?:package\.json|pnpm-lock\.yaml|vite\.config\.ts|vitest\.config\.ts)$|api\/openapi\/admin\.openapi\.json$|scripts\/completion\/operator-smoke\.mjs$)/.test(path)))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';
}
try {
  if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);
  const context=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);
  if(context.status!==0)throw new Error('Local Docker context unavailable.'); requireLocalEngine(context.stdout.trim());
  tenant=loadLocalProfile('identity').TENANT_ID;if(!uuid(tenant))throw new Error('Local tenant is invalid.');
  mkdirSync(resolve(repositoryRoot,'.local/completion/operator-verification'),{recursive:true});
  output=mkdtempSync(resolve(repositoryRoot,'.local/completion/operator-verification/run-'));
  const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);
  report={check_id:'local-operator-administration',started_at:new Date().toISOString(),result:'RUNNING',source_manifest_sha256:createHash('sha256').update(before).digest('hex'),checks,
    note:'Actual HTTPS browser and persistent local BFF/PostgreSQL. A uniquely owned root fixture and signed local MFA assertion are used; configured Firebase MFA/provider enrollment is not claimed.'};
  sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${subjectID}','${rootProvider}','Synthetic operator QA',ARRAY['SUPER_ADMIN'],ARRAY['IN']);`);
  bootstrapped=true;
  const secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
  const body=JSON.stringify({subject_id:subjectID,session_id:randomUUID(),tenant_id:tenant,display_name:'Synthetic operator QA',roles:['SUPER_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
  const timestamp=String(Math.floor(Date.now()/1000));
  const signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
  const exchange=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',body,headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},signal:AbortSignal.timeout(10000)});
  check('fresh-local-mfa-exchange',exchange.status===201);cookie=exchange.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('No secure session returned.');
  browser=await chromium.launch({headless:true});
  for(const viewport of [{width:1366,height:900},{width:390,height:844}]) {
    const context=await browser.newContext({ignoreHTTPSErrors:true,viewport});const separator=cookie.indexOf('=');
    await context.addCookies([{name:cookie.slice(0,separator),value:cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);
    const page=await context.newPage();lastPage=page;await page.goto(origin+'/operators');
    try {await page.getByRole('heading',{name:'Operator directory',exact:true}).waitFor();}
    catch(error) {await page.screenshot({path:resolve(output,'failure.png'),fullPage:true});writeFileSync(resolve(output,'failure-ui.txt'),(await page.locator('body').innerText()).slice(0,4000));throw error;}
    if(viewport.width===1366) {
      await page.getByLabel('Name',{exact:true}).fill(name);await page.getByLabel('Firebase user ID',{exact:true}).fill(providerSubject);
      await page.getByLabel('Reason for access change').fill('Local acceptance: provision regional operator');
      const creation=page.waitForResponse(response=>new URL(response.url()).pathname==='/admin/api/v1/operators' && response.request().method()==='POST');
      await page.getByRole('button',{name:'Create operator',exact:true}).click();
      const created=await creation;const account=await created.json();check('ui-create-current-country-operator',created.status()===201&&account.roles.length===1&&account.roles[0]==='REGIONAL_STAFF'&&account.allowed_countries.join()==='IN'&&account.revision===1);
    }
    await page.getByLabel('Search by name or account ID').fill(name);await page.getByRole('button',{name:'Search operators',exact:true}).click();
    await page.getByRole('button',{name:`Edit access: ${name}`,exact:true}).waitFor();
    check(`scoped-directory-persists-${viewport.width}`,true);
    await page.getByRole('button',{name:`Edit access: ${name}`,exact:true}).click();
    await page.getByLabel('Status',{exact:true}).selectOption(viewport.width===1366?'false':'true');
    await page.getByLabel('Reason for access change').fill(viewport.width===1366?'Local acceptance: suspend operator':'Local acceptance: restore operator');
    const change=page.waitForResponse(response=>new URL(response.url()).pathname.startsWith('/admin/api/v1/operators/')&&response.request().method()==='PUT');
    await page.getByRole('button',{name:'Save access',exact:true}).click();const changed=await change;const value=await changed.json();
    check(`ui-access-revision-${viewport.width}`,changed.status()===200&&value.active===(viewport.width!==1366)&&value.revision===(viewport.width===1366?2:3));
    await page.getByRole('button',{name:`Access history: ${name}`,exact:true}).click();
    await page.getByText('Local acceptance: provision regional operator',{exact:true}).waitFor();
    await page.getByText('Local acceptance: suspend operator',{exact:true}).waitFor();
    check(`audit-readback-${viewport.width}`,true);
    check(`responsive-no-horizontal-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
    const accessibility=await new AxeBuilder({page}).analyze();
    check(`accessibility-no-serious-critical-${viewport.width}`,!accessibility.violations.some(violation=>violation.impact==='serious'||violation.impact==='critical'));
    await page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo({top:0,behavior:'instant'});});
    await page.evaluate(()=>new Promise(resolveFrame=>requestAnimationFrame(()=>requestAnimationFrame(resolveFrame))));
    await page.screenshot({path:resolve(output,`operators-${viewport.width}.png`),fullPage:true});
    await context.close();
  }
  // Direct commands use the same persistent authority, not UI visibility alone.
  const headers={Cookie:cookie,Origin:origin,'Content-Type':'application/json'};
  const session=await fetch('http://127.0.0.1:8090/admin/api/v1/session',{headers,signal:AbortSignal.timeout(10000)});const view=await session.json();headers['X-CSRF-Token']=view.csrf_token;
  const input={display_name:'Denied operator',provider_subject:`denied-${runID}`,roles:['SUPER_ADMIN'],allowed_countries:['IN'],active:true,revision:0,reason:'Local negative acceptance'};
  const create=async body=>fetch('http://127.0.0.1:8090/admin/api/v1/operators',{method:'POST',headers,body:JSON.stringify(body),signal:AbortSignal.timeout(10000)});
  check('direct-root-grant-denied',(await create(input)).status===422);
  check('direct-country-expansion-denied',(await create({...input,roles:['AUDITOR'],allowed_countries:['IN','NG']})).status===403);
  check('client-tenant-injection-denied',(await create({...input,roles:['AUDITOR'],tenant_id:randomUUID()})).status===422);
  const noCSRF=await fetch('http://127.0.0.1:8090/admin/api/v1/operators',{method:'POST',headers:{...headers,'X-CSRF-Token':'invalid'},body:JSON.stringify({...input,roles:['AUDITOR']}),signal:AbortSignal.timeout(10000)});
  check('direct-csrf-bypass-denied',noCSRF.status===403);
  report.source_unchanged=manifest()===before;check('source-unchanged',report.source_unchanged);
} catch(error) {failure=error.message;if(lastPage&&!lastPage.isClosed()&&output){try{await lastPage.screenshot({path:resolve(output,'failure.png'),fullPage:true});writeFileSync(resolve(output,'failure-ui.txt'),(await lastPage.locator('body').innerText()).slice(0,8000));}catch{ /* Keep the original assertion failure. */ }}}
finally {
  await browser?.close();
  if(bootstrapped) {
    try {
      // Exact generated binding, not a prefix or broad role/country predicate.
      // Preserve the created operator and its audit, leaving it suspended.
      sql(`BEGIN; WITH changed AS (UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}' AND provider_subject='${providerSubject}' AND active RETURNING *) INSERT INTO admin.operator_events(id,tenant_id,subject_id,actor_id,country,action,revision,roles,allowed_countries,active,reason,occurred_at) SELECT '${randomUUID()}',tenant_id,subject_id,'${subjectID}','IN','UPDATED',revision,roles,allowed_countries,active,'Local acceptance owned fixture cleanup',now() FROM changed; DELETE FROM admin.sessions WHERE tenant_id='${tenant}' AND subject_id IN (SELECT subject_id FROM admin.operator_accounts WHERE tenant_id='${tenant}' AND provider_subject IN ('${providerSubject}','${rootProvider}')); DELETE FROM admin.operator_accounts WHERE tenant_id='${tenant}' AND subject_id='${subjectID}' AND provider_subject='${rootProvider}'; COMMIT;`);
      checks.push({id:'owned-fixture-cleanup',result:'PASSED'});
    } catch(error) {failure??=error.message;}
  }
  if(report) {report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;if(!failure)report.screenshots=[1366,390].map(width=>({file:`operators-${width}.png`,sha256:createHash('sha256').update(readFileSync(resolve(output,`operators-${width}.png`))).digest('hex')}));writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
  console.log(JSON.stringify({result:failure?'FAILED':'PASSED',checks:checks.length,...(output?{evidence:resolve(output,'report.json')}:{}),...(failure?{failure}:{})},null,2));process.exitCode=failure?2:0;
}
