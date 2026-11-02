import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {render,screen,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {MemoryRouter,Outlet,Route,Routes} from "react-router";
import {afterEach,expect,it,vi} from "vitest";
import {PrivacyPage} from "./privacy-page";
import {adminSession} from "../test/fixtures";
import type {AdminSession} from "../api/client";

const adminID="10000000-0000-4000-8000-000000000001";
const otherID="10000000-0000-4000-8000-000000000002";
const proposalID="20000000-0000-4000-8000-000000000001";
const requestID="30000000-0000-4000-8000-000000000001";
const privacySession:AdminSession={...adminSession,subject_id:adminID,roles:["COUNTRY_ADMIN"]};
const proposal={id:proposalID,owner:"identity",expected_revision:0,policy_version:"retention-v1",retention_microseconds:86_400_000_000,reason_reference:"governance-case-001",status:"PENDING",requested_by:otherID,requested_at:"2026-10-28T08:00:00Z"};
const deletion={id:requestID,identity_id:"40000000-0000-4000-8000-000000000001",status:"SCHEDULED",requested_at:"2026-10-28T08:00:00Z",effective_at:"2026-11-28T08:00:00Z",ready_owners:1,blocked_owners:2,active_holds:0};
const hold={id:7,request_id:requestID,owner:"identity",reason_reference:"court-order-001",recorded_by:otherID,recorded_at:"2026-10-28T09:00:00Z",revision:1};

function show(session=privacySession){
 const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});
 return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={["/"]}><Routes><Route element={<Outlet context={session}/>}><Route path="/" element={<PrivacyPage/>}/></Route></Routes></MemoryRouter></QueryClientProvider>);
}
const problem=(status:number)=>Response.json({error:{code:"ADMIN_PRIVACY_GOVERNANCE_FORBIDDEN",message:"Current authority expired",correlation_id:"test-correlation",retryable:false}},{status});
const lists=(path:string)=>path.endsWith("/retention-proposals")?{items:[proposal]}:path.endsWith("/deletions")?{items:[deletion]}:{items:[hold]};
afterEach(()=>{vi.unstubAllGlobals();});

it("does not load private records without country policy authority or fresh MFA",()=>{
 const fetch=vi.fn();vi.stubGlobal("fetch",fetch);
 show({...privacySession,roles:["REGIONAL_STAFF"]});
 expect(screen.getByText("Privacy governance access unavailable")).toBeVisible();
 expect(fetch).not.toHaveBeenCalled();
 show({...privacySession,assurance:{...privacySession.assurance,fresh_auth:false}});
 expect(screen.getByRole("link",{name:"Verify administrator identity"})).toHaveAttribute("href","/login?reauth=mfa");
 expect(fetch).not.toHaveBeenCalled();
});

it("reviews country policy with four eyes and sends only scoped command fields",async()=>{
 const writes:{path:string;init:RequestInit}[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){writes.push({path,init});return Promise.resolve(Response.json({id:proposalID,status:"PUBLISHED",revision:1}));}
  if(path.endsWith("/retention-proposals/"+proposalID))return Promise.resolve(Response.json(proposal));
  return Promise.resolve(Response.json(lists(path)));
 }));
 show();
 expect(await screen.findByText("identity · retention-v1")).toBeVisible();
 expect(screen.getByText(/Country control plane · IN/)).toBeVisible();
 await userEvent.click(screen.getByRole("button",{name:"Review publication: identity"}));
 expect(screen.getByText("Confirm retention publication")).toBeVisible();
 await userEvent.click(screen.getByRole("button",{name:"Publish reviewed proposal"}));
 await screen.findByText("Retention policy published for this country.");
 expect(writes).toHaveLength(1);
 expect(writes[0]?.path).toBe(`/admin/api/v1/privacy/retention-proposals/${proposalID}/publish`);
 expect(new Headers(writes[0]?.init.headers).get("X-CSRF-Token")).toBe(privacySession.csrf_token);
 expect(writes[0]?.init.body).toBeUndefined();
 expect(new Headers(writes[0]?.init.headers).has("Authorization")).toBe(false);
});

it("clears country records when the server revokes the operator during a command",async()=>{
 let attempts=0;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){
   attempts++;
   return Promise.resolve(problem(403));
  }
  return Promise.resolve(Response.json(lists(path)));
 }));
 show();await screen.findByText("Request "+requestID);
 await userEvent.type(screen.getByLabelText("Data owner",{exact:true}),"supply");
 await userEvent.type(screen.getByLabelText("Policy version"),"retention-v2");
 await userEvent.type(screen.getByLabelText("Retention days"),"7");
 await userEvent.type(screen.getByLabelText("Decision reference"),"policy-case-002");
 await userEvent.click(screen.getByRole("button",{name:"Record proposal"}));
 expect(await screen.findByRole("alert")).toHaveTextContent("Current authority expired");
 expect(screen.queryByText("Request "+requestID)).not.toBeInTheDocument();
 expect(attempts).toBe(1);
});

it("retains the proposal idempotency key across a transient owner error",async()=>{
 const attempts:{id:string;owner:string;retention_seconds:number;expected_revision:number;reason_reference:string}[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){
   const body=JSON.parse(init.body as string) as typeof attempts[number];attempts.push(body);
   if(attempts.length===1)return Promise.resolve(Response.json({error:{code:"ADMIN_PRIVACY_GOVERNANCE_UNAVAILABLE",message:"Retry later",correlation_id:"test",retryable:true}},{status:503}));
   return Promise.resolve(Response.json({id:body.id,status:"PROPOSED"},{status:201}));
  }
  return Promise.resolve(Response.json(lists(path)));
 }));
 show();await screen.findByText("Request "+requestID);
 await userEvent.type(screen.getByLabelText("Data owner",{exact:true}),"identity");
 await userEvent.type(screen.getByLabelText("Policy version"),"retention-v2");
 await userEvent.type(screen.getByLabelText("Retention days"),"7");
 await userEvent.type(screen.getByLabelText("Decision reference"),"policy-case-002");
 await userEvent.click(screen.getByRole("button",{name:"Record proposal"}));
 await screen.findByText(/Retry later/);
 await userEvent.click(screen.getByRole("button",{name:"Record proposal"}));
 await screen.findByText(/Retention proposal recorded/);
 expect(attempts).toHaveLength(2);
 expect(attempts[0]).toEqual(attempts[1]);
 expect(attempts[0]?.retention_seconds).toBe(604800);
 expect(attempts[0]?.expected_revision).toBe(0);
 expect(attempts[0]?.owner).toBe("identity");
});

it("records a scoped hold and requires a separate release reference and revision",async()=>{
 const writes:{path:string;body:Record<string,unknown>;headers:Headers}[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){
   writes.push({path,body:JSON.parse(init.body as string) as Record<string,unknown>,headers:new Headers(init.headers)});
   return Promise.resolve(path.endsWith("/release")?Response.json({id:7,status:"RELEASED",revision:2}):Response.json({id:8,status:"ACTIVE",revision:1},{status:201}));
  }
  if(path.endsWith("/deletions/"+requestID))return Promise.resolve(Response.json(deletion));
  return Promise.resolve(Response.json(lists(path)));
 }));
 show();await screen.findByText("Hold 7 · identity");
 await userEvent.click(screen.getByRole("button",{name:"Place legal hold"}));
 const holdForm=screen.getByRole("group",{name:`Hold request ${requestID}`});
 await userEvent.type(within(holdForm).getByLabelText("Data owner"),"identity");
 await userEvent.type(within(holdForm).getByLabelText("Legal reference"),"court-order-002");
 await userEvent.click(within(holdForm).getByRole("button",{name:"Record legal hold"}));
 await screen.findByText("Legal hold recorded for the selected request.");
 expect(writes[0]?.path).toBe(`/admin/api/v1/privacy/deletions/${requestID}/holds`);
 expect(writes[0]?.body).toEqual({owner:"identity",reason_reference:"court-order-002"});
 await userEvent.click(screen.getByRole("button",{name:"Review release"}));
 const releaseForm=screen.getByRole("group",{name:"Release hold 7"});
 await userEvent.type(within(releaseForm).getByLabelText("Release reference"),"approved-release-002");
 await userEvent.click(within(releaseForm).getByRole("button",{name:"Confirm independent release"}));
 await screen.findByText("Legal hold released after independent review.");
 expect(writes[1]?.path).toBe("/admin/api/v1/privacy/holds/7/release");
 expect(writes[1]?.body).toEqual({expected_revision:1,reason_reference:"approved-release-002"});
 expect(writes.every(value=>value.headers.get("X-CSRF-Token")===privacySession.csrf_token)).toBe(true);
});
