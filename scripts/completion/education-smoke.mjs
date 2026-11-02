// Actual local HTTPS UI/BFF acceptance. Synthetic tenant and MFA assertions;
// this is not a configured Firebase authentication or native-device claim.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import AxeBuilder from '../../admin-web/node_modules/@axe-core/playwright/dist/index.mjs';
import {createHash,createHmac,randomUUID} from 'node:crypto';
import {mkdirSync,mkdtempSync,readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';

const origin='https://localhost:4173';
const tenant=randomUUID(),subject=randomUUID();
const checks=[];
let browser,context,cookie,output,report,failure;
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Education acceptance failed: ${id}`);}
function manifest(){
  const files=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z','--','internal/configcms','migrations/identity','internal/identity','admin-web/src','api/openapi/identity.openapi.json','scripts/completion/education-smoke.mjs'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});
  if(files.status!==0)throw new Error('Source inventory unavailable.');
  return [...new Set(files.stdout.split('\0').filter(Boolean))].sort().map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';
}
try{
  mkdirSync(resolve(repositoryRoot,'.local/completion/education-verification'),{recursive:true});
  output=mkdtempSync(resolve(repositoryRoot,'.local/completion/education-verification/run-'));
  const before=manifest();writeFileSync(resolve(output,'source.sha256'),before);
  report={check_id:'local-onboarding-administration',result:'RUNNING',started_at:new Date().toISOString(),checks,
    source_manifest_sha256:createHash('sha256').update(before).digest('hex'),
    note:'Real HTTPS desktop/mobile browser, BFF and PostgreSQL drafts in a unique synthetic tenant. Local signed MFA assertion only; no real Firebase challenge or device/provider approval is claimed.'};
  const body=JSON.stringify({subject_id:subject,session_id:randomUUID(),tenant_id:tenant,display_name:'Synthetic education QA',roles:['COUNTRY_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
  const timestamp=String(Math.floor(Date.now()/1000));
  const secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
  const signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
  const exchange=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},body,signal:AbortSignal.timeout(10000)});
  check('fresh-local-mfa-exchange',exchange.status===201);
  cookie=exchange.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('Session cookie unavailable.');
  browser=await chromium.launch({headless:true});
  for(const viewport of [{width:1366,height:900},{width:390,height:844}]){
    context=await browser.newContext({ignoreHTTPSErrors:true,viewport});
    const separator=cookie.indexOf('=');await context.addCookies([{name:cookie.slice(0,separator),value:cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);
    const page=await context.newPage();await page.goto(origin+'/cms');await page.getByRole('heading',{name:'CMS page builder',exact:true}).waitFor();
    await page.getByRole('button',{name:'Create mobile introduction',exact:true}).click();
    const role=viewport.width===1366?'vendor':'rider';
    await page.getByLabel('Application role',{exact:true}).selectOption(role);
    await page.getByLabel('Introduction language',{exact:true}).selectOption('ta');
    await page.getByLabel('Introduction title',{exact:true}).fill('வணக்கம்');
    await page.getByLabel('Introduction message',{exact:true}).fill('உங்கள் அனுமதிகளைத் தேர்வு செய்யுங்கள்');
    const response=page.waitForResponse(item=>item.request().method()==='PUT'&&new URL(item.url()).pathname.endsWith(`/mobile-onboarding-${role}-ta/draft`));
    await page.getByRole('button',{name:'Save introduction draft',exact:true}).click();
    const saved=await response;const draft=await saved.json();
    check(`ui-creates-scoped-tamil-${role}-draft`,saved.status()===200&&draft.tenant_id===tenant&&draft.country==='IN'&&draft.revision===1&&draft.page.audience.join()===role.toUpperCase()&&draft.page.blocks[0].content.title==='வணக்கம்');
    await page.getByText('Introduction draft saved. Review it in the page editor before submitting publication.',{exact:true}).waitFor();
    check(`draft-not-auto-published-${viewport.width}`,!await page.getByText('Page publication executed.',{exact:true}).count());
    check(`no-horizontal-overflow-${viewport.width}`,await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
    const axe=await new AxeBuilder({page}).analyze();
    check(`no-serious-critical-accessibility-${viewport.width}`,!axe.violations.some(item=>item.impact==='serious'||item.impact==='critical'));
    await page.evaluate(()=>{if(document.activeElement instanceof HTMLElement)document.activeElement.blur();window.scrollTo({top:0,behavior:'instant'});});
    await page.evaluate(()=>new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done))));
    await page.screenshot({path:resolve(output,`education-${viewport.width}.png`),fullPage:true});
    const session=await context.request.get(origin+'/admin/api/v1/session');const sessionData=await session.json();
    const denied=await context.request.put(origin+`/admin/api/v1/cms/pages/mobile-onboarding-${role}-ta/draft`,{headers:{Origin:origin},data:{expected_revision:1,page:draft.page}});
    check(`direct-draft-write-without-csrf-denied-${viewport.width}`,denied.status()===403);
    check(`session-country-stays-scoped-${viewport.width}`,sessionData.selected_country==='IN'&&sessionData.allowed_countries.join()==='IN');
    await context.close();context=null;
  }
  report.source_unchanged=before===manifest();check('source-unchanged',report.source_unchanged);
}catch(error){failure=error.message;}
finally{
  if(cookie){
    try{
      const cleanupContext=context??await browser?.newContext({ignoreHTTPSErrors:true});
      if(cleanupContext){
        const separator=cookie.indexOf('=');await cleanupContext.addCookies([{name:cookie.slice(0,separator),value:cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);
        const session=await cleanupContext.request.get(origin+'/admin/api/v1/session');const value=await session.json();
        const revoked=await cleanupContext.request.delete(origin+'/admin/api/v1/session',{headers:{Origin:origin,'X-CSRF-Token':value.csrf_token}});
        check('owned-synthetic-session-revoked',revoked.status()===204);await cleanupContext.close();
      }
    }catch(error){failure??=error.message;}
  }
  await browser?.close();
  if(report){report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
  console.log(JSON.stringify({result:failure?'FAILED':'PASSED',checks:checks.length,evidence:output?resolve(output,'report.json'):null,...(failure?{failure}:{})},null,2));process.exitCode=failure?2:0;
}
