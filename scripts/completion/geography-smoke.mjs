// Real local admin UI and normal Flutter HTTP acceptance. All identities/MFA
// are uniquely owned synthetic fixtures; no launch or financial defaults.
import {chromium} from '../../admin-web/node_modules/@playwright/test/index.mjs';
import {createHash,createHmac,randomUUID} from 'node:crypto';
import {existsSync,mkdirSync,mkdtempSync,readFileSync,writeFileSync,unlinkSync,openSync,closeSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {loadLocalProfile} from './runtime.mjs';
import {stackDirectory} from './stack.mjs';
import {requireLocalEngine} from './verify.mjs';
const mobile=resolve(repositoryRoot,'../planext4u-moble'),origin='https://localhost:4173';
const run=randomUUID(),subject=randomUUID(),provider=`geography-admin-${run}`;
const checks=[],path=[],sources=[];
let tenant,cookie,csrf,browser,customer,output,report,privateFile,owned=false,root,originalRoot,rootCreated=false,failure;
const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
function check(id,ok){checks.push({id,result:ok?'PASSED':'FAILED'});if(!ok)throw new Error(`Geography acceptance failed: ${id}`);}
const uuid=value=>/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);
function sql(text){const result=command(['compose','--project-name','planext4u-phase0','-f','deploy/local/compose.yaml','-f',resolve(stackDirectory,'compose.json'),'exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],{input:text});if(result.status!==0)throw new Error('Owned geography fixture operation failed.');}
async function call(target,method='GET',body,{admin=false,token=customer?.tokens?.access_token,headers={}}={}){
 const response=await fetch(`http://127.0.0.1:${admin?'8090':'8082'}${target}`,{method,headers:{'Content-Type':'application/json',...(admin?{Cookie:cookie,Origin:origin,'X-CSRF-Token':csrf??''}:token?{Authorization:`Bearer ${token}`}:{}) ,...headers},...(body===undefined?{}:{body:JSON.stringify(body)}),signal:AbortSignal.timeout(15000)});
 const text=await response.text();return {status:response.status,body:text?JSON.parse(text):{}};
}
async function adminSession(){
 const source=randomUUID();sources.push(source);
 const secret=readFileSync(loadLocalProfile('admin').ADMIN_EXCHANGE_SECRET_FILE);
 const body=JSON.stringify({subject_id:subject,session_id:source,tenant_id:tenant,display_name:'Synthetic geography journey',roles:['COUNTRY_ADMIN'],allowed_countries:['IN'],selected_country:'IN',authenticated_at:new Date().toISOString(),auth_methods:['pwd','totp']});
 const timestamp=String(Math.floor(Date.now()/1000)),signature=createHmac('sha256',secret).update(`v1\n${timestamp}\n${body}`).digest('hex');
 const response=await fetch('http://127.0.0.1:8090/internal/v1/admin-sessions',{method:'POST',body,headers:{'Content-Type':'application/json','X-P4U-Admin-Timestamp':timestamp,'X-P4U-Admin-Signature':`v1=${signature}`},signal:AbortSignal.timeout(10000)});
 if(response.status!==201)throw new Error('Synthetic administrator session unavailable');
 cookie=response.headers.get('set-cookie')?.split(';')[0];if(!cookie)throw new Error('Secure admin cookie unavailable');
 sql(`DO $$ BEGIN UPDATE admin.sessions s SET operator_revision=a.revision FROM admin.operator_accounts a WHERE s.tenant_id='${tenant}' AND s.subject_id='${subject}' AND s.session_id='${source}' AND a.tenant_id=s.tenant_id AND a.subject_id=s.subject_id AND a.active; IF NOT FOUND THEN RAISE EXCEPTION 'Owned geography source unavailable'; END IF; END $$;`);
 const session=await call('/admin/api/v1/session','GET',undefined,{admin:true});if(session.status!==200)throw new Error('Current directory session unavailable');csrf=session.body.csrf_token;
}
async function writeMaster(value,active,reason){return call(`/admin/api/v1/masters/${value.id}`,'PUT',{...value,active,reason},{admin:true});}
function sourceManifest(){
 const files=spawnSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});if(files.status!==0)throw new Error('Source inventory unavailable');
 const selected=[...new Set(files.stdout.split('\0').filter(file=>/^(?:cmd\/|internal\/|migrations\/|api\/openapi\/|admin-web\/src\/|scripts\/completion\/(?:geography-smoke\.mjs|fixtures\/geography-client\.dart)$)/.test(file)))].sort();
 const result=selected.map(file=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,file))).digest('hex')}  backend/${file}`);
 for(const file of ['apps/customer/lib/main.dart','packages/experience/lib/src/geography.dart','packages/experience/lib/src/geography_picker.dart','packages/experience/lib/src/transactions.dart','packages/experience/lib/src/transaction_screens.dart','build/live_geography_journey_test.dart'])result.push(`${createHash('sha256').update(readFileSync(resolve(mobile,file))).digest('hex')}  mobile/${file}`);
 return result.join('\n')+'\n';
}
async function createViaUI(page,kind,code,name,parent){
 await page.getByRole('combobox',{name:/^Master type/}).selectOption(kind);
 if(parent){
  await page.getByLabel('Search parent names or code',{exact:true}).fill(parent.code);
  await page.getByLabel(/^Parent entry /).selectOption(parent.id);
 }
 if(kind!=='COUNTRY')await page.getByLabel('Stable code',{exact:true}).fill(code);
 await page.getByLabel('English name',{exact:true}).fill(name);
 await page.getByLabel('Tamil name',{exact:true}).fill(kind==='COUNTRY'?'இந்தியா':kind==='CITY'?'சென்னை':'சோதனை பகுதி');
 await page.getByLabel('Reason for change',{exact:true}).fill('Uniquely owned local address geography acceptance');
 const pending=page.waitForResponse(response=>new URL(response.url()).pathname.startsWith('/admin/api/v1/masters/')&&response.request().method()==='PUT');
 await page.getByRole('button',{name:'Save master data',exact:true}).click();
 const response=await pending,value=await response.json();
 check(`normal-admin-ui-created-${kind.toLowerCase()}`,response.status()===200&&uuid(value.id)&&value.kind===kind&&value.code===code&&(!parent||value.parent_id===parent.id));
 return value;
}
try{
 const fixture=readFileSync(resolve(repositoryRoot,'scripts/completion/fixtures/geography-client.dart'));
 mkdirSync(resolve(mobile,'build'),{recursive:true});const harness=resolve(mobile,'build/live_geography_journey_test.dart');
 if(existsSync(harness)&&!readFileSync(harness).equals(fixture))throw new Error('Owned geography harness differs; inspect before replacement');writeFileSync(harness,fixture);
 if(process.env.DOCKER_HOST)requireLocalEngine(process.env.DOCKER_HOST);const context=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);if(context.status!==0)throw new Error('Local Docker unavailable');requireLocalEngine(context.stdout.trim());
 tenant=loadLocalProfile('identity').TENANT_ID;if(!uuid(tenant))throw new Error('Invalid local tenant');
 mkdirSync(resolve(repositoryRoot,'.local/completion/geography-journey'),{recursive:true});output=mkdtempSync(resolve(repositoryRoot,'.local/completion/geography-journey/run-'));
 const before=sourceManifest();writeFileSync(resolve(output,'source.sha256'),before);
 report={check_id:'normal-admin-customer-geography-journey',started_at:new Date().toISOString(),result:'RUNNING',checks,source_manifest_sha256:createHash('sha256').update(before).digest('hex'),note:'Actual normal HTTPS admin hierarchy authoring and normal Flutter/IoApiTransport address selection/save/sign-in/readback. Unique synthetic local provider/MFA fixtures; no wallet/slot requests or financial seed/reset. Current Android device/provider evidence is separate.'};
 sql(`INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries) VALUES('${tenant}','${subject}','${provider}','Synthetic geography journey',ARRAY['COUNTRY_ADMIN'],ARRAY['IN']);`);owned=true;
 await adminSession();check('current-country-admin-session',true);
 const roots=await call('/admin/api/v1/masters?kind=COUNTRY&status=ALL','GET',undefined,{admin:true});check('normal-country-directory',roots.status===200);root=roots.body.items.find(item=>item.code==='IN');
 if(root){originalRoot={...root};if(!root.active){const enabled=await writeMaster(root,true,'Temporarily enable existing country for isolated local acceptance');check('normal-country-reactivation',enabled.status===200);root=enabled.body;}}
 browser=await chromium.launch({headless:true});const ctx=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1366,height:900}}),separator=cookie.indexOf('=');
 await ctx.addCookies([{name:cookie.slice(0,separator),value:cookie.slice(separator+1),url:origin,secure:true,httpOnly:true,sameSite:'Strict'}]);const page=await ctx.newPage();await page.goto(origin+'/masters');await page.getByRole('heading',{name:'Master data',exact:true}).waitFor();
 if(!root){root=await createViaUI(page,'COUNTRY','IN','India');rootCreated=true;}
 path.push(root);
 for(const kind of ['STATE','DISTRICT','CITY','AREA']){const code=`QA_${kind}_${run.replaceAll('-','').toUpperCase()}`;path.push(await createViaUI(page,kind,code,kind==='CITY'?'Chennai':`Local ${kind.toLowerCase()} ${run.slice(0,8)}`,path.at(-1)));}
 await ctx.close();
 const providerToken=`synthetic-participant-${run}`;
 const auth=await call('/v1/auth/exchange','POST',{provider:'local',provider_token:providerToken,device_id:`geography-${run}`,country:'IN'},{token:null});check('normal-customer-sign-in',auth.status===201&&Boolean(auth.body.tokens?.access_token));customer=auth.body;
 privateFile=resolve(mobile,`build/geography-${run}.private.json`);writeFileSync(privateFile,JSON.stringify({base_url:'http://127.0.0.1:8082',access_token:customer.tokens.access_token,provider_token:providerToken,second_device:`geography-relogin-${run}`,path}),{mode:0o600});
 const runner=resolve(output,'run-client.ps1');writeFileSync(runner,"$ErrorActionPreference='Stop'\nSet-Location 'C:\\planext4u\\planext4u-moble'\n& 'C:\\tmp\\planext4u-review-tools\\flutter\\bin\\flutter.bat' test --no-pub build/live_geography_journey_test.dart --reporter expanded\nexit $LASTEXITCODE\n");
 const fd=openSync(resolve(output,'normal-flutter-http.log'),'w',0o600);let client;
 try{client=spawnSync('powershell.exe',['-NoProfile','-NonInteractive','-File',runner],{cwd:mobile,env:{...process.env,PLANEXT_GEOGRAPHY_CONFIG:privateFile},stdio:['ignore',fd,fd],windowsHide:true,timeout:180000});}finally{closeSync(fd);}
 check('normal-flutter-hierarchy-address-save-signin-readback-without-finance',client.status===0);
 let book=await call('/v1/addresses');check('normal-own-address-readback',book.status===200&&book.body.items.length===1);let address=book.body.items[0];
 const input={label:'Home',line1:'456 Updated Local Road',postal_code:'600001',locality:'Chennai',default:true,geography_area_id:path.at(-1).id};
 const mutate=(method,target,body,revision)=>call(target,method,body,{headers:{'Idempotency-Key':randomUUID(),...(revision===undefined?{}:{'If-Match':`"${revision}"`})}});
 check('country-query-authority-denied',(await call('/v1/geography?kind=COUNTRY&country=US')).status===422);
 check('wrong-parent-returns-no-children',(await call(`/v1/geography?kind=AREA&parent_id=${root.id}`)).body.items.length===0);
 check('caller-snapshot-rejected',(await mutate('POST','/v1/addresses',{...input,geography:{area_id:path.at(-1).id,path}},undefined)).status===422);
 check('unknown-reference-rejected',(await mutate('POST','/v1/addresses',{...input,geography_area_id:randomUUID()},undefined)).status===422);
 check('wrong-kind-reference-rejected',(await mutate('POST','/v1/addresses',{...input,geography_area_id:path[3].id},undefined)).status===422);
 let area=path.at(-1);const retired=await writeMaster(area,false,'Retire owned local address area');check('normal-admin-retirement',retired.status===200);area=retired.body;
 const hidden=await call(`/v1/geography?kind=AREA&parent_id=${path[3].id}&search=${area.code}`);check('retired-area-hidden',hidden.status===200&&hidden.body.items.length===0);
 book=await call('/v1/addresses');check('retired-snapshot-retained',book.body.items[0].geography.path[4].master_revision===1);
 check('retired-new-selection-denied',(await mutate('POST','/v1/addresses',input,undefined)).status===422);
 const retained=await mutate('PATCH',`/v1/addresses/${address.id}`,input,address.revision);check('retired-existing-address-edit',retained.status===200&&retained.body.geography.path[4].master_revision===1);address=retained.body;
 check('stale-address-edit-denied',(await mutate('PATCH',`/v1/addresses/${address.id}`,input,1)).status===409);
 const active=await writeMaster(area,true,'Restore owned area to verify default-address readback');check('normal-admin-reactivation',active.status===200);area=active.body;
 const second=await mutate('POST','/v1/addresses',{...input,label:'Other'},undefined);check('normal-second-default-address',second.status===201&&second.body.default&&second.body.geography.path[4].master_revision===3);
 book=await call('/v1/addresses');const previous=book.body.items.find(item=>item.id===address.id);check('implicit-default-change-has-new-revision',book.body.items.filter(item=>item.default).length===1&&!previous.default&&previous.revision===address.revision+1);
 check('stale-default-device-edit-denied',(await mutate('PATCH',`/v1/addresses/${address.id}`,input,address.revision)).status===409);
 check('normal-default-address-removal',(await mutate('DELETE',`/v1/addresses/${second.body.id}`,undefined,second.body.revision)).status===204);
 book=await call('/v1/addresses');address=book.body.items[0];check('default-promotion-preserves-saved-snapshot',book.body.items.length===1&&address.default&&address.revision===previous.revision+1&&address.geography.path[4].master_revision===1);
 check('normal-last-address-removal',(await mutate('DELETE',`/v1/addresses/${address.id}`,undefined,address.revision)).status===204);
 check('normal-empty-address-book',(await call('/v1/addresses')).body.items.length===0);
 check('normal-customer-logout',(await call('/v1/auth/revoke','POST',{refresh_token:customer.tokens.refresh_token},{token:null})).status===204);
 check('revoked-customer-address-read-denied',(await call('/v1/addresses')).status===401);
 report.source_unchanged=sourceManifest()===before;check('source-unchanged',report.source_unchanged);
}catch(error){failure=error.message;}
finally{
 await browser?.close();if(privateFile&&existsSync(privateFile))unlinkSync(privateFile);
 if(customer){try{
  const current=await call('/v1/addresses');if(current.status===200){for(const address of current.body.items.filter(item=>item.geography?.area_id===path.at(-1)?.id).sort((a,b)=>Number(a.default)-Number(b.default))){const removed=await call(`/v1/addresses/${address.id}`,'DELETE',undefined,{headers:{'Idempotency-Key':randomUUID(),'If-Match':`"${address.revision}"`}});if(removed.status!==204)throw new Error('Owned address cleanup failed');}}
  await call('/v1/auth/revoke','POST',{refresh_token:customer.tokens.refresh_token},{token:null});
 }catch{failure??='Owned customer fixture cleanup failed';}}
 if(owned){try{
  await adminSession();
  for(const item of path.slice(1).reverse()){
   const found=await call(`/admin/api/v1/masters?kind=${item.kind}&status=ALL&search=${item.code}`,'GET',undefined,{admin:true});const current=found.body.items?.find(value=>value.id===item.id);
   if(current?.active){const result=await writeMaster(current,false,'Retire uniquely owned geography acceptance fixture');if(result.status!==200)throw new Error('Owned master cleanup failed');}
  }
  if(root&&(rootCreated||originalRoot&&!originalRoot.active)){
   const found=await call('/admin/api/v1/masters?kind=COUNTRY&status=ALL','GET',undefined,{admin:true});const current=found.body.items?.find(item=>item.id===root.id);
   if(current?.active){const result=await writeMaster(current,false,'Restore country activation after owned local acceptance');if(result.status!==200)throw new Error('Country state restoration failed');}
  }
  sql(`BEGIN; DELETE FROM admin.sessions WHERE tenant_id='${tenant}' AND subject_id='${subject}'; DELETE FROM admin.operator_accounts WHERE tenant_id='${tenant}' AND subject_id='${subject}' AND provider_subject='${provider}'; COMMIT;`);
 }catch{failure??='Owned administrator fixture cleanup failed';}}
 if(output){report??={checks};report.result=failure?'FAILED':'PASSED';report.finished_at=new Date().toISOString();if(failure)report.failure=failure;writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({result:report.result,checks:checks.length,evidence:resolve(output,'report.json'),...(failure?{failure}:{})},null,2));}else if(failure)console.error(failure);
}
process.exitCode=failure?1:0;
