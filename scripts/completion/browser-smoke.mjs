import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import {readFileSync, writeFileSync} from 'node:fs';
import {createHmac, randomUUID} from 'node:crypto';
import {resolve} from 'node:path';
import {loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';

// Synthetic local assertion, exchanged through the real BFF. No API interception.
const origin = 'https://localhost:4173';
const secret = readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
const body = JSON.stringify({subject_id:randomUUID(),session_id:randomUUID(),tenant_id:loadLocalProfile('identity').TENANT_ID,
  display_name:'Synthetic local QA',roles:['SUPER_ADMIN'],allowed_countries:['IN'],selected_country:'IN',
  authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
const timestamp = String(Math.floor(Date.now()/1000));
const signature = createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
const exchange = await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',body,headers:{
  'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`}});
if (exchange.status!==201) throw new Error(`Synthetic administrator exchange failed: ${exchange.status}`);
const cookie = exchange.headers.get('set-cookie').split(';')[0];
const separator = cookie.indexOf('=');
let browser;
const results = [];
let failure;
try {
  browser = await chromium.launch({headless:true});
  for (const viewport of [{width:1366,height:900},{width:390,height:844}]) {
    // Only the explicit localhost certificate is self-signed. No remote target.
    const context = await browser.newContext({ignoreHTTPSErrors:true,viewport});
    await context.addCookies([{name:cookie.slice(0,separator),value:cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);
    const page = await context.newPage();
    for (const [path,heading,apiPath] of [['/operations','Privileged operations','/admin/api/v1/operations'],['/cms','CMS page builder','/admin/api/v1/cms/pages'],['/organization','Organization and staff','/admin/api/v1/organization']]) {
      const responsePromise = page.waitForResponse(response=>new URL(response.url()).pathname===apiPath,{timeout:15000});
      await page.goto(origin+path);
      const response = await responsePromise;
      await page.getByRole('heading',{name:heading,exact:true}).waitFor();
      if(response.status()!==200) throw new Error(`${path} returned ${response.status()}`);
      results.push({path,width:viewport.width,status:response.status(),result:'PASSED'});
    }
    await page.screenshot({path:resolve(stackDirectory,`admin-${viewport.width}.png`),fullPage:true});
    await context.close();
  }
} catch(error) {failure=error.message;}
finally {
  await browser?.close();
  const session = await fetch('http://127.0.0.1:8090/admin/api/v1/session',{headers:{Cookie:cookie}});
  if(session.status===200) {
    const view=await session.json();
    const logout=await fetch('http://127.0.0.1:8090/admin/api/v1/session',{method:'DELETE',headers:{Cookie:cookie,Origin:origin,'X-CSRF-Token':view.csrf_token}});
    if(logout.status!==204) failure??='Synthetic browser session cleanup failed';
  }
  const report={check_id:'admin-live-browser',checked_at:new Date().toISOString(),result:failure?'FAILED':'PASSED',checks:results,...(failure?{failure}:{}),
    note:'Real HTTPS UI and persistent BFF on desktop/mobile Chromium viewports. Synthetic local MFA assertion; no mocked responses or external identity-provider challenge.'};
  writeFileSync(resolve(stackDirectory,'browser-smoke.json'),`${JSON.stringify(report,null,2)}\n`);
  console.log(JSON.stringify(report,null,2));
  process.exitCode=failure?2:0;
}
