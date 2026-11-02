import AxeBuilder from "@axe-core/playwright";
import {expect,test,type Page,type Route} from "@playwright/test";
import {adminSession} from "../src/test/fixtures";
import type {AdminSession} from "../src/api/client";

const first="10000000-0000-4000-8000-000000000001";
const second="10000000-0000-4000-8000-000000000002";
const requestID="30000000-0000-4000-8000-000000000001";
const now="2026-10-28T08:00:00Z";
const countrySession=(id=first):AdminSession=>({...adminSession,subject_id:id,roles:["COUNTRY_ADMIN"],navigation:[...adminSession.navigation,{id:"privacy",label:"Privacy governance",path:"/privacy",capability:"admin.policy.manage"}]});
type Proposal={id:string;owner:string;expected_revision:number;policy_version:string;retention_microseconds:number;reason_reference:string;status:"PENDING"|"PUBLISHED";requested_by:string;requested_at:string;approved_by?:string;published_at?:string};
type Hold={id:number;request_id:string;owner:string;reason_reference:string;recorded_by:string;recorded_at:string;revision:number};

async function fixture(page:Page,{failFirstProposal=false}={}){
 const state:{session:AdminSession;proposals:Proposal[];holds:Hold[];proposalBodies:Record<string,unknown>[];holdBodies:Record<string,unknown>[];releaseBodies:Record<string,unknown>[];reads:number;failFirstProposal:boolean}={session:countrySession(),proposals:[],holds:[],proposalBodies:[],holdBodies:[],releaseBodies:[],reads:0,failFirstProposal};
 const deletion={id:requestID,identity_id:"40000000-0000-4000-8000-000000000001",status:"SCHEDULED",requested_at:now,effective_at:"2026-11-28T08:00:00Z",ready_owners:0,blocked_owners:2,active_holds:0};
 const send=(route:Route,body:unknown,status=200)=>route.fulfill({status,contentType:"application/json",headers:{"Cache-Control":"no-store"},body:JSON.stringify(body)});
 const problem={error:{code:"ADMIN_PRIVACY_GOVERNANCE_UNAVAILABLE",message:"Synthetic provider response lost",correlation_id:"synthetic-privacy",retryable:true}};
 await page.route("**/admin/api/v1/**",async route=>{
  const path=new URL(route.request().url()).pathname,method=route.request().method();
  if(path==="/admin/api/v1/session")return send(route,state.session);
  if(!path.startsWith("/admin/api/v1/privacy/"))return send(route,problem,404);
  state.reads+=method==="GET"?1:0;
  if(path==="/admin/api/v1/privacy/retention-proposals"){
   if(method==="GET")return send(route,{items:state.proposals});
   const body=route.request().postDataJSON() as Record<string,unknown>;state.proposalBodies.push(body);
   if(state.failFirstProposal){state.failFirstProposal=false;return send(route,problem,503);}
   if(!state.proposals.some(value=>value.id===body.id))state.proposals.push({id:String(body.id),owner:String(body.owner),expected_revision:Number(body.expected_revision),policy_version:String(body.policy_version),retention_microseconds:Number(body.retention_seconds)*1_000_000,reason_reference:String(body.reason_reference),status:"PENDING",requested_by:state.session.subject_id,requested_at:now});
   return send(route,{id:body.id,status:"PROPOSED"},201);
  }
  if(path==="/admin/api/v1/privacy/deletions")return send(route,{items:state.session.selected_country==="IN"?[{...deletion,active_holds:state.holds.length}]:[]});
  if(path===`/admin/api/v1/privacy/deletions/${requestID}`)return send(route,state.session.selected_country==="IN"?{...deletion,active_holds:state.holds.length}:problem,state.session.selected_country==="IN"?200:404);
  if(path==="/admin/api/v1/privacy/holds")return send(route,{items:state.session.selected_country==="IN"?state.holds:[]});
  const proposalPath=/^\/admin\/api\/v1\/privacy\/retention-proposals\/([^/]+)(\/publish)?$/.exec(path);
  if(proposalPath){const value=state.proposals.find(item=>item.id===proposalPath[1]);if(!value)return send(route,problem,404);if(method==="GET")return send(route,value);if(value.requested_by===state.session.subject_id)return send(route,problem,403);value.status="PUBLISHED";value.approved_by=state.session.subject_id;value.published_at=now;return send(route,{id:value.id,status:"PUBLISHED",revision:value.expected_revision+1});}
  if(path===`/admin/api/v1/privacy/deletions/${requestID}/holds` && method==="POST"){
   const body=route.request().postDataJSON() as Record<string,unknown>;state.holdBodies.push(body);
   const hold:Hold={id:7,request_id:requestID,owner:String(body.owner),reason_reference:String(body.reason_reference),recorded_by:state.session.subject_id,recorded_at:now,revision:1};state.holds=[hold];
   return send(route,{id:hold.id,status:"ACTIVE",revision:1},201);
  }
  if(path==="/admin/api/v1/privacy/holds/7/release" && method==="POST"){
   const body=route.request().postDataJSON() as Record<string,unknown>;state.releaseBodies.push(body);
   if(state.holds[0]?.recorded_by===state.session.subject_id)return send(route,problem,403);
   state.holds=[];return send(route,{id:7,status:"RELEASED",revision:2});
  }
  return send(route,problem,404);
 });
 return state;
}

test("country administrator retries a proposal and a different administrator publishes it",async({page})=>{
 const state=await fixture(page,{failFirstProposal:true});
 await page.goto("/privacy");
 await expect(page.getByRole("heading",{name:"Privacy governance"})).toBeVisible();
 await expect(page.getByText(/Country control plane · IN/)).toBeVisible();
 await page.getByLabel("Data owner",{exact:true}).fill("identity");
 await page.getByLabel("Policy version").fill("reviewed-v1");
 await page.getByLabel("Retention days").fill("7");
 await page.getByLabel("Decision reference").fill("policy-case-123");
 await page.getByRole("button",{name:"Record proposal"}).click();
 await expect(page.getByRole("alert")).toContainText("Synthetic provider response lost");
 await page.getByRole("button",{name:"Record proposal"}).click();
 await expect(page.getByText(/Retention proposal recorded/)).toBeVisible();
 expect(state.proposalBodies).toHaveLength(2);
 expect(state.proposalBodies[0]).toEqual(state.proposalBodies[1]);
 expect(state.proposalBodies[0]).toMatchObject({owner:"identity",policy_version:"reviewed-v1",retention_seconds:604800,expected_revision:0});
 await expect(page.getByRole("button",{name:"Another administrator must publish"})).toBeDisabled();
 state.session=countrySession(second);
 await page.reload();
 await page.getByRole("button",{name:"Review publication: identity"}).click();
 await expect(page.getByText("Confirm retention publication")).toBeVisible();
 await page.getByRole("button",{name:"Publish reviewed proposal"}).click();
 await expect(page.getByText(/Retention policy published/)).toBeVisible();
 await expect(page.getByText(/PUBLISHED · revision 0 → 1/)).toBeVisible();
 expect(state.proposals[0]?.approved_by).toBe(second);
 const results=await new AxeBuilder({page}).analyze();
 expect(results.violations.filter(value=>["serious","critical"].includes(value.impact??""))).toEqual([]);
});

test("hold placement is visible and independent release removes the active hold",async({page})=>{
 const state=await fixture(page);
 await page.goto("/privacy");
 await expect(page.getByText("Request "+requestID)).toBeVisible();
 await page.getByRole("button",{name:"Place legal hold"}).click();
 const form=page.getByRole("group",{name:"Hold request "+requestID});
 await form.getByLabel("Data owner").fill("identity");
 await form.getByLabel("Legal reference").fill("court-order-123");
 await form.getByRole("button",{name:"Record legal hold"}).click();
 await expect(page.getByText("Hold 7 · identity")).toBeVisible();
 await expect(page.getByRole("button",{name:"Another administrator must release"})).toBeDisabled();
 state.session=countrySession(second);
 await page.reload();
 await page.getByRole("button",{name:"Review release"}).click();
 const release=page.getByRole("group",{name:"Release hold 7"});
 await release.getByLabel("Release reference").fill("release-order-456");
 await release.getByRole("button",{name:"Confirm independent release"}).click();
 await expect(page.getByText("No active holds.")).toBeVisible();
 expect(state.holdBodies).toEqual([{owner:"identity",reason_reference:"court-order-123"}]);
 expect(state.releaseBodies).toEqual([{expected_revision:1,reason_reference:"release-order-456"}]);
});

test("stale MFA and unrelated roles cannot fetch deletion requests",async({page})=>{
 const state=await fixture(page);
 state.session={...countrySession(),assurance:{...adminSession.assurance,fresh_auth:false}};
 await page.goto("/privacy");
 await expect(page.getByRole("heading",{name:"Privacy governance requires fresh MFA"})).toBeVisible();
 expect(state.reads).toBe(0);
 state.session={...countrySession(),roles:["CONTENT_ADMIN"]};
 await page.reload();
 await expect(page.getByText("Privacy governance access unavailable")).toBeVisible();
 expect(state.reads).toBe(0);
 state.session={...countrySession(),roles:["REGIONAL_STAFF"]};
 await page.reload();
 await expect(page.getByText("Privacy governance access unavailable")).toBeVisible();
 expect(state.reads).toBe(0);
});
