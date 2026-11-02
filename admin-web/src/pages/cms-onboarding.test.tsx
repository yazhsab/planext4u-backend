import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { CMSPage } from "./cms-page";
import { adminSession, cmsWorkspaceDraft, operationPage } from "../test/fixtures";
import type {CMSPage as PageDefinition} from "../api/client";

afterEach(() => {vi.unstubAllGlobals();});
function show() {
  const client = new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});
  render(<QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<Outlet context={adminSession} />}><Route index element={<CMSPage />} /></Route></Routes></MemoryRouter></QueryClientProvider>);
}

it("creates a role/language onboarding draft with CSRF and no publication side effect", async () => {
  const requests: {url:string;init?:RequestInit}[]=[];
  vi.stubGlobal("fetch",vi.fn((url:string,init?:RequestInit)=>{
    requests.push({url,init});
    if(init?.method === "PUT") {
      const body=JSON.parse(init.body as string) as {page:unknown};
      return Promise.resolve(Response.json({tenant_id:"tenant-test",country:"IN",page:body.page,revision:1,updated_by:adminSession.subject_id,updated_at:"2026-10-25T12:00:00Z"}));
    }
    return Promise.resolve(Response.json({items:[]}));
  }));
  show();
  const user=userEvent.setup();
  await user.click(screen.getByRole("button",{name:"Create mobile introduction"}));
  await user.selectOptions(screen.getByLabelText("Application role"),"rider");
  await user.selectOptions(screen.getByLabelText("Introduction language"),"ta");
  await user.type(screen.getByLabelText("Introduction title"),"வணக்கம்");
  await user.type(screen.getByLabelText("Introduction message"),"உங்கள் அனுமதிகளைத் தேர்வு செய்யுங்கள்");
  await user.click(screen.getByRole("button",{name:"Save introduction draft"}));
  await screen.findByText("Introduction draft saved. Review it in the page editor before submitting publication.");
  const writes=requests.filter(item=>item.init?.method==="PUT");
  expect(writes).toHaveLength(1);
  expect(writes[0]?.url).toContain("mobile-onboarding-rider-ta/draft");
  const body=JSON.parse(writes[0]?.init?.body as string) as {expected_revision:number;page:{audience:string[];route:string;blocks:{kind:string;content:Record<string,string>}[]}};
  expect(body.expected_revision).toBe(0);
  expect(body.page.audience).toEqual(["RIDER"]);
  expect(body.page.route).toBe("/app/onboarding/rider/ta");
  expect(body.page.blocks[0]).toMatchObject({kind:"ONBOARDING",content:{version:"1",title:"வணக்கம்"}});
  expect(new Headers(writes[0]?.init?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
  expect(requests.some(item=>item.url.includes("/operations"))).toBe(false);
});

it("keeps the introduction editable when a duplicate draft is rejected", async () => {
  vi.stubGlobal("fetch",vi.fn((_url:string,init?:RequestInit)=>Promise.resolve(init?.method==="PUT"
    ? Response.json({error:{code:"CMS_REVISION_CONFLICT",message:"An introduction draft already exists.",correlation_id:"corr-test",retryable:false}},{status:409})
    : Response.json({items:[]}))));
  show();
  const user=userEvent.setup();
  await user.click(screen.getByRole("button",{name:"Create mobile introduction"}));
  await user.type(screen.getByLabelText("Introduction title"),"Welcome");
  await user.type(screen.getByLabelText("Introduction message"),"Choose your permissions.");
  await user.click(screen.getByRole("button",{name:"Save introduction draft"}));
  expect(await screen.findByRole("alert")).toHaveTextContent("changed on the server");
  expect(screen.getByLabelText("Introduction message")).toHaveValue("Choose your permissions.");
});

const audienceGroup={id:"c0685b62-abdb-4696-aed0-6977d2ac8dc5",node_id:"a49e6218-6eac-4ec8-8c97-c831db9e43e7",name:"West participants",role_ids:["8eaad005-231e-42d6-9a83-c0e65aac07c7"],include_descendants:true,active:true,revision:1};
const audienceDraft=(groups:string[]=[])=>({tenant_id:"synthetic-audience-tenant",country:"IN",revision:1,updated_by:adminSession.subject_id,updated_at:"2026-10-25T12:00:00Z",page:{id:"audience-page",route:"/audience-page",title_key:"audience.page",audience:["CUSTOMER"],audience_groups:groups,enabled:true,blocks:[]} as PageDefinition});

it("saves selected dynamic groups without publishing or expanding the base role audience",async()=>{
 let draft=audienceDraft();let sent:{expected_revision:number;page:PageDefinition}|undefined;
 const fetch=vi.fn((path:string,init?:RequestInit)=>{
  if(path.endsWith("/audiences"))return Promise.resolve(Response.json({groups:[audienceGroup]}));
  if(init?.method==="PUT"){sent=JSON.parse(init.body as string) as typeof sent;if(!sent)throw new Error("Missing page body");draft={...draft,revision:2,page:sent.page};return Promise.resolve(Response.json(draft));}
  return Promise.resolve(Response.json({items:[draft]}));
 });vi.stubGlobal("fetch",fetch);show();
 await userEvent.click(await screen.findByLabelText(audienceGroup.name));await userEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft revision 2 saved.");
 expect(sent).toMatchObject({expected_revision:1,page:{audience:["CUSTOMER"],audience_groups:[audienceGroup.id]}});expect(fetch.mock.calls.some(([path])=>path.includes("/operations"))).toBe(false);
});

it("retains existing group restrictions when the group directory is unavailable",async()=>{
 let draft=audienceDraft([audienceGroup.id]);let sent:PageDefinition|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(path.endsWith("/audiences"))return Promise.resolve(Response.json({error:{code:"UNAVAILABLE",message:"Unavailable",correlation_id:"test",retryable:true}},{status:503}));
  if(init?.method==="PUT"){const body=JSON.parse(init.body as string) as {page:PageDefinition};sent=body.page;draft={...draft,page:body.page,revision:2};return Promise.resolve(Response.json(draft));}
  return Promise.resolve(Response.json({items:[draft]}));
 }));show();await screen.findByText(/Existing group selection retained/);await userEvent.type(screen.getByLabelText("Title key"),".updated");await userEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft revision 2 saved.");expect(sent?.audience_groups).toEqual([audienceGroup.id]);
});

it("retains selected restrictions across pages and saves a group loaded later",async()=>{
 const later={...audienceGroup,id:"d0685b62-abdb-4696-aed0-6977d2ac8dc5",name:"Later audience"};
 let draft=audienceDraft([audienceGroup.id]);let sent:PageDefinition|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(path.includes("/audiences"))return Promise.resolve(Response.json(path.includes("?after=")?{groups:[later]}:{groups:[audienceGroup],next_cursor:audienceGroup.id}));
  if(init?.method==="PUT"){const body=JSON.parse(init.body as string) as {page:PageDefinition};sent=body.page;draft={...draft,page:body.page,revision:2};return Promise.resolve(Response.json(draft));}
  return Promise.resolve(Response.json({items:[draft]}));
 }));show();expect(await screen.findByLabelText(audienceGroup.name)).toBeChecked();
 await userEvent.click(screen.getByRole("button",{name:"Load more audience groups"}));await userEvent.click(await screen.findByLabelText(later.name));
 expect(screen.getByLabelText(audienceGroup.name)).toBeChecked();await userEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft revision 2 saved.");expect(sent?.audience_groups).toEqual([audienceGroup.id,later.id]);
});

it("creates a public role/language startup splash as an unpublished bounded draft",async()=>{
 const writes:{path:string;body:{page:PageDefinition;expected_revision:number}}[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="PUT"){
   const body=JSON.parse(init.body as string) as {page:PageDefinition;expected_revision:number};writes.push({path,body});
   return Promise.resolve(Response.json({...audienceDraft(),page:body.page}));
  }
  return Promise.resolve(Response.json(path.endsWith("/audiences")?{groups:[]}:{items:[]}));
 }));show();await userEvent.click(screen.getByRole("button",{name:"Create startup splash"}));
 await userEvent.selectOptions(screen.getByLabelText("Startup application role"),"vendor");await userEvent.selectOptions(screen.getByLabelText("Startup splash language"),"ta");
 await userEvent.type(screen.getByLabelText("Startup splash title"),"வணக்கம்");await userEvent.type(screen.getByLabelText("Startup splash message"),"Planext4u வரவேற்கிறது");await userEvent.click(screen.getByRole("button",{name:"Save startup splash draft"}));
 await screen.findByText("Startup splash draft saved. Review it in the page editor before submitting publication.");
 expect(writes).toHaveLength(1);expect(writes[0]?.path).toContain("mobile-splash-vendor-ta/draft");expect(writes[0]?.body).toMatchObject({expected_revision:0,page:{route:"/app/splash/vendor/ta",audience:["PUBLIC"],blocks:[{kind:"ONBOARDING",content:{version:"1",title:"வணக்கம்",message:"Planext4u வரவேற்கிறது"}}]}});
 expect(writes[0]?.body.page.audience_groups).toBeUndefined();
});

it("keeps a startup splash public and prevents audience or route retargeting in the editor",async()=>{
 const draft={...audienceDraft(),page:{...audienceDraft().page,id:"mobile-splash-customer-en",route:"/app/splash/customer/en",audience:["PUBLIC"],blocks:[{id:"splash",kind:"ONBOARDING",enabled:true,priority:0,content:{version:"1",title:"Welcome",message:"Public message"}}]} as PageDefinition};
 vi.stubGlobal("fetch",vi.fn((path:string)=>Promise.resolve(Response.json(path.endsWith("/audiences")?{groups:[audienceGroup]}:{items:[draft]}))));show();
 expect(await screen.findByLabelText("Route")).toHaveAttribute("readonly");expect(screen.getByLabelText("PUBLIC")).toBeChecked();expect(screen.getByLabelText("PUBLIC")).toBeDisabled();expect(screen.getByLabelText("CUSTOMER")).toBeDisabled();expect(screen.queryByLabelText(audienceGroup.name)).not.toBeInTheDocument();expect(screen.getByRole("button",{name:"Add block"})).toBeDisabled();
});

it("can create the next startup language using stable labels after the first save",async()=>{
 const pages:PageDefinition[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="PUT"){
   const body=JSON.parse(init.body as string) as {page:PageDefinition};pages.push(body.page);
   return Promise.resolve(Response.json({...audienceDraft(),page:body.page}));
  }
  return Promise.resolve(Response.json(path.endsWith("/audiences")?{groups:[]}:{items:[]}));
 }));show();await userEvent.click(screen.getByRole("button",{name:"Create startup splash"}));
 await userEvent.type(screen.getByLabelText("Startup splash title",{exact:true}),"Welcome");
 await userEvent.type(screen.getByLabelText("Startup splash message",{exact:true}),"Welcome to Planext4u.");
 await userEvent.click(screen.getByRole("button",{name:"Save startup splash draft"}));
 await screen.findByText("Startup splash draft saved. Review it in the page editor before submitting publication.");
 await userEvent.selectOptions(screen.getByLabelText("Startup splash language"),"ta");
 await userEvent.clear(screen.getByLabelText("Startup splash message",{exact:true}));
 await userEvent.type(screen.getByLabelText("Startup splash message",{exact:true}),"வணக்கம்");
 await userEvent.click(screen.getByRole("button",{name:"Save startup splash draft"}));
 await waitFor(()=>{expect(pages).toHaveLength(2);});
 expect(pages.map(page=>page.id)).toEqual(["mobile-splash-customer-en","mobile-splash-customer-ta"]);
 expect(pages[1]?.blocks[0]?.content).toMatchObject({message:"வணக்கம்"});
});

it("requests page publication with the current draft and owning snapshot revisions",async()=>{
 let command:Record<string,unknown>|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(path.endsWith("/audiences"))return Promise.resolve(Response.json({groups:[]}));
  if(init?.method==="POST"){
   command=JSON.parse(init.body as string) as Record<string,unknown>;
   return Promise.resolve(Response.json({...operationPage.changes[0],command:{...command,correlation_id:"synthetic-cms-publication"}}));
  }
  return Promise.resolve(Response.json({items:[{...audienceDraft(),revision:3}],published_revision:7}));
 }));show();await screen.findByLabelText("Title key");
 await userEvent.type(screen.getByLabelText("Publication reason"),"Verified public welcome content");
 await userEvent.click(screen.getByRole("button",{name:"Request publication"}));
 await screen.findByText("Publication submitted for independent approval.");
 expect(command).toMatchObject({domain:"CMS",action:"PUBLISH",target_id:"audience-page",payload:{publication_type:"PAGE",expected_draft_revision:3,expected_snapshot_revision:7}});
});

it("keeps publication disabled when the owning snapshot revision is unavailable",async()=>{
 const fetch=vi.fn((path:string)=>Promise.resolve(Response.json(path.endsWith("/audiences")?{groups:[]}:{items:[audienceDraft()]})));
 vi.stubGlobal("fetch",fetch);show();await screen.findByLabelText("Title key");
 await userEvent.type(screen.getByLabelText("Publication reason"),"Verified public welcome content");
 expect(screen.getByRole("button",{name:"Request publication"})).toBeDisabled();
 expect(screen.getByText("Refresh publication details before publishing.")).toBeInTheDocument();
 expect(fetch.mock.calls.some(([path])=>path.includes("/operations"))).toBe(false);
});

it("requests the first workspace publication with snapshot revision zero",async()=>{
 let command:Record<string,unknown>|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){
   command=JSON.parse(init.body as string) as Record<string,unknown>;
   return Promise.resolve(Response.json({...operationPage.changes[0],command:{...command,correlation_id:"synthetic-cms-publication"}}));
  }
  if(path.endsWith("/cms/workspace"))return Promise.resolve(Response.json({...cmsWorkspaceDraft,published_revision:0}));
  return Promise.resolve(Response.json(path.endsWith("/audiences")?{groups:[]}:{items:[]}));
 }));show();await userEvent.click(screen.getByRole("tab",{name:"App workspace"}));
 await screen.findByLabelText("Publication reason");
 await userEvent.type(screen.getByLabelText("Publication reason"),"Verified initial app configuration");
 await userEvent.click(screen.getByRole("button",{name:"Request publication"}));
 await screen.findByText("Workspace publication submitted for independent approval.");
 expect(command).toMatchObject({domain:"CMS",action:"PUBLISH",target_id:"workspace",payload:{publication_type:"WORKSPACE",expected_draft_revision:4,expected_snapshot_revision:0}});
});

it("saves contact matching as an optional versioned draft without publishing it",async()=>{
 let submitted:{expected_revision:number;workspace:typeof cmsWorkspaceDraft.workspace}|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="PUT"&&path.endsWith("/cms/workspace/draft")){
   submitted=JSON.parse(init.body as string) as typeof submitted;
   return Promise.resolve(Response.json({...cmsWorkspaceDraft,revision:5,workspace:submitted?.workspace}));
  }
  return Promise.resolve(Response.json(path.endsWith("/cms/workspace")?cmsWorkspaceDraft:{items:[]}));
 }));
 show();const user=userEvent.setup();
 await user.click(screen.getByRole("tab",{name:"App workspace"}));
 await user.click(await screen.findByRole("button",{name:"Add optional contact matching policy"}));
 const version=screen.getByLabelText("Contact matching policy version");
 await user.clear(version);await user.type(version,"contacts-2026.1");
 expect(screen.getByRole("button",{name:"Request publication"})).toBeDisabled();
 await user.click(screen.getByRole("button",{name:"Save draft"}));
 await waitFor(()=>{expect(submitted?.workspace.consent_policies).toEqual([
  cmsWorkspaceDraft.workspace.consent_policies[0],
  {purpose:"CONTACT_MATCHING",policy_version:"contacts-2026.1",required:false},
 ]);});
 expect(submitted?.expected_revision).toBe(4);
});
