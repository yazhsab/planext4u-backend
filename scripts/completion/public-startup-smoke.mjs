// Normal local CMS browser publication and anonymous gateway acceptance.
// Only the operator authentication assertions are synthetic. No remote evidence.
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

const origin='https://localhost:4173',tenant=loadLocalProfile('identity').TENANT_ID;
const run=randomUUID(),maker=randomUUID(),checker=randomUUID(),checks=[],cookies=[];
let browser,owner,approver,page,output,report,failure,fixtures=false;
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Startup acceptance failed: ${id}`);}
function command(args,options={}){return spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});}
function sql(source){const value=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:source});if(value.status!==0)throw new Error('Owned startup fixture operation failed.');}
function manifest(){const value=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z','--','internal/configcms','internal/gateway','internal/adminshell','internal/organization','cmd/configuration','cmd/gateway','cmd/admin','api/openapi/configuration.openapi.json','api/openapi/admin.openapi.json','admin-web/src','scripts/completion/public-startup-smoke.mjs'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(value.status!==0)throw new Error('Startup source inventory unavailable.');return [...new Set(value.stdout.split('\0').filter(Boolean))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';}
async function session(subject){
 const source=randomUUID(),body=JSON.stringify({subject_id:subject,session_id:source,tenant_id:tenant,display_name:'Synthetic startup QA',roles:['COUNTRY_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE),signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const response=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body,signal:AbortSignal.timeout(10000)});check('synthetic-local-mfa-exchange',response.status===201);
 const cookie=response.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('Owned startup session unavailable.');cookies.push(cookie);
 sql(`DO $$ BEGIN UPDATE admin.sessions s SET operator_revision=a.revision FROM admin.operator_accounts a WHERE s.session_id='${source}' AND s.tenant_id='${tenant}' AND s.subject_id='${subject}' AND a.tenant_id=s.tenant_id AND a.subject_id=s.subject_id AND a.active; IF NOT FOUND THEN RAISE EXCEPTION 'Owned startup session unavailable'; END IF; END $$;`);
 const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1366,height:900}}),i=cookie.indexOf('=');context.setDefaultTimeout(15000);await context.addCookies([{name:cookie.slice(0,i),value:cookie.slice(i+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);return context;
}
async function headers(context){const response=await context.request.get(origin+'/admin/api/v1/session');check('current-operator-session-resolved',response.status()===200);return {Origin:origin,'X-CSRF-Token':(await response.json()).csrf_token};}
async function startup(role,locale,extra=''){return fetch(`http://127.0.0.1:8082/v1/startup?country=IN&role=${role}&locale=${locale}${extra}`,{signal:AbortSignal.timeout(10000)});}
try{
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);const engine=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(engine.status!==0)throw new Error('Local engine unavailable.');requireLocalEngine(engine.stdout.trim());
 if(!/^[a-f0-9-]{36}$/.test(tenant??''))throw new Error('Installed local tenant unavailable.');
 mkdirSync(resolve(repositoryRoot,'.local/completion/startup-verification'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/startup-verification/run-'));
 const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);writeFileSync(resolve(output,'fixture-identities.private.json'),JSON.stringify({tenant,maker,checker,run}));
 report={check_id:'local-public-startup',result:'RUNNING',published_count:0,started_at:new Date().toISOString(),checks,source_manifest_sha256:createHash('sha256').update(before).digest('hex'),external_provider_verified:false,note:'Real HTTPS browser and normal CMS page creation/publication commands with independent approval. Signed local MFA is synthetic. Only exact owned operator/session fixtures are retired; welcome content remains in the installed local tenant. Financial/workspace configuration and prior introductions are preserved.'};
 sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${maker}','startup-maker-${run}','Synthetic startup maker',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']),('${tenant}','${checker}','startup-checker-${run}','Synthetic startup approver',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']);`);fixtures=true;
 browser=await chromium.launch({headless:true});owner=await session(maker);approver=await session(checker);const makerHeaders=await headers(owner),checkerHeaders=await headers(approver);
 page=await owner.newPage();await page.goto(origin+'/cms');
 const initial=await owner.request.get(origin+'/admin/api/v1/cms/pages'),initialDrafts=await initial.json();check('owning-publication-revision-available',initial.status()===200&&Number.isSafeInteger(initialDrafts.published_revision)&&initialDrafts.published_revision>0);
 const initialRevision=initialDrafts.published_revision;report.initial_snapshot_revision=initialRevision;
 for(const role of ['customer','vendor','rider'])for(const locale of ['en','ta']){
  const id=`mobile-splash-${role}-${locale}`,title=`Planext4u ${role[0].toUpperCase()+role.slice(1)}`,message=locale==='ta'?'தொடர உள்நுழையவும் அல்லது கணக்கை உருவாக்கவும்.':'Welcome. Sign in or create your account to continue.';
  // Reruns preserve existing content; unexpected unrelated content is never overwritten.
  const existing=initialDrafts.items.find(value=>value.page.id===id);
  if(existing){check(`${role}-${locale}-existing-content-preserved`,existing.page.blocks?.[0]?.content?.message===message);}
  else{
   const creator=page.getByRole('region',{name:'Mobile startup splash drafts',exact:true});
   if(!await creator.getByLabel('Startup application role',{exact:true}).count())await creator.getByRole('button',{name:'Create startup splash',exact:true}).click();
   await creator.getByLabel('Startup application role',{exact:true}).selectOption(role);await creator.getByLabel('Startup splash language',{exact:true}).selectOption(locale);await creator.getByLabel('Startup splash title',{exact:true}).fill(title);await creator.getByLabel('Startup splash message',{exact:true}).fill(message);
   const saving=page.waitForResponse(value=>value.request().method()==='PUT'&&value.url().endsWith(`/cms/pages/${id}/draft`));await creator.getByRole('button',{name:'Save startup splash draft',exact:true}).click();check(`${role}-${locale}-normal-browser-draft`,(await saving).status()===200);
  }
  await page.getByRole('button').filter({hasText:`/app/splash/${role}/${locale}`}).click();
  const editor=page.getByRole('region',{name:`Edit splash.${role}.${locale}`,exact:true});await editor.getByRole('button',{name:'Refresh publication details',exact:true}).click();
  await editor.getByLabel('Publication reason',{exact:true}).fill('Publish reviewed public welcome text for local Android acceptance');
  const submitted=page.waitForResponse(value=>value.request().method()==='POST'&&value.url()===origin+'/admin/api/v1/operations');await editor.getByRole('button',{name:'Request publication',exact:true}).click();const response=await submitted,change=await response.json();
  check(`${role}-${locale}-normal-browser-publication-request`,response.status()===201&&change.status==='PENDING_APPROVAL'&&change.command.payload.publication_type==='PAGE'&&change.command.payload.expected_snapshot_revision===initialRevision+report.published_count);
  check(`${role}-${locale}-self-approval-denied`,(await owner.request.post(origin+`/admin/api/v1/operations/${change.id}/approve`,{headers:makerHeaders,data:{expected_revision:change.revision}})).status()===409);
  const approval=await approver.request.post(origin+`/admin/api/v1/operations/${change.id}/approve`,{headers:checkerHeaders,data:{expected_revision:change.revision}});check(`${role}-${locale}-independent-owner-publication`,approval.status()===200&&(await approval.json()).status==='EXECUTED');
  report.published_count=(report.published_count??0)+1;
  const anonymous=await startup(role,locale),document=await anonymous.json();
  check(`${role}-${locale}-anonymous-current-public-text`,anonymous.status===200&&document.revision===initialRevision+report.published_count&&document.role===role&&document.locale===locale&&document.splash?.title===title&&document.splash?.message===message&&Object.keys(document).sort().join(',')==='country,locale,published_at,revision,role,splash'&&Object.keys(document.splash).sort().join(',')==='message,title,version'&&anonymous.headers.get('cache-control')==='no-store');
 }
 check('anonymous-bootstrap-still-denied',(await fetch('http://127.0.0.1:8082/v1/bootstrap',{signal:AbortSignal.timeout(10000)})).status===401);
 check('caller-tenant-query-denied',(await startup('customer','en','&tenant_id='+randomUUID())).status===422);
 check('repeated-country-query-denied',(await startup('customer','en','&country=NG')).status===422);
 check('unsupported-role-denied',(await startup('admin','en')).status===422);
 check('uninstalled-country-denied',(await fetch('http://127.0.0.1:8082/v1/startup?country=NG&role=customer',{signal:AbortSignal.timeout(10000)})).status===404);
 const forged=await fetch('http://127.0.0.1:8082/v1/startup?country=IN&role=customer&locale=ta',{headers:{Authorization:'Bearer forged','X-Planext4u-Tenant':randomUUID(),'X-Planext4u-Country':'NG','X-Planext4u-Roles':'SUPER_ADMIN'},signal:AbortSignal.timeout(10000)});check('forged-identity-does-not-expand-public-contract',forged.status===200&&(await forged.json()).country==='IN');
 const finalDrafts=await (await owner.request.get(origin+'/admin/api/v1/cms/pages')).json();
 check('existing-unrelated-drafts-preserved',initialDrafts.items.filter(value=>!value.page.id.startsWith('mobile-splash-')).every(value=>JSON.stringify(finalDrafts.items.find(after=>after.page.id===value.page.id))===JSON.stringify(value)));
 await page.getByRole('region',{name:'Mobile startup splash drafts',exact:true}).getByRole('button',{name:'Create startup splash',exact:true}).click();
 for(const [name,width,large] of [['desktop',1366,false],['mobile',390,false],['large-text',390,true]]){
  await page.setViewportSize({width,height:900});if(large)await page.addStyleTag({content:':root{font-size:200%}'});await page.evaluate(()=>window.scrollTo(0,0));
  check(`${name}-no-horizontal-overflow`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));const axe=await new AxeBuilder({page}).analyze();check(`${name}-no-serious-accessibility-violations`,!axe.violations.some(value=>['serious','critical'].includes(value.impact)));await page.screenshot({path:resolve(output,`startup-${name}.png`),fullPage:true});
 }
 check('source-unchanged',before===manifest());report.source_unchanged=before===manifest();report.visual_review='PENDING';
}catch(error){failure=error.message;if(page&&!page.isClosed()&&output){try{await page.screenshot({path:resolve(output,'failure.png'),fullPage:true});writeFileSync(resolve(output,'failure-ui.private.txt'),(await page.locator('body').innerText()).slice(0,14000));}catch{}}}
finally{
 for(const context of [owner,approver]){if(context){try{check('owned-session-revoked',(await context.request.delete(origin+'/admin/api/v1/session',{headers:await headers(context)})).status()===204);}catch(error){failure??=error.message;}await context.close();}}
 await browser?.close();
 if(fixtures){try{sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${tenant}' AND subject_id IN('${maker}','${checker}'); UPDATE admin.operator_accounts SET active=false,revision=revision+1 WHERE tenant_id='${tenant}' AND subject_id IN('${maker}','${checker}'); COMMIT;`);check('only-owned-operators-retired',true);}catch(error){failure??=error.message;}}
 if(report){report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
}
console.log(JSON.stringify({result:failure?'FAILED':'PASSED',checks:checks.length,evidence:output,...(failure?{failure}:{})},null,2));if(failure)process.exitCode=1;
