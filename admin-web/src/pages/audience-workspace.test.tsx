import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {act,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {AudienceWorkspace} from "./audience-workspace";
import type {AudienceGroup} from "../api/audiences";
import type {OrganizationWorkspace} from "../api/organization";
import type {AdminSession} from "../api/client";
import {adminSession} from "../test/fixtures";

afterEach(()=>{vi.unstubAllGlobals();localStorage.clear();});
const node="a49e6218-6eac-4ec8-8c97-c831db9e43e7",role="8eaad005-231e-42d6-9a83-c0e65aac07c7",participant="94e0aba8-6554-41c6-8996-ecf7462207f9";
const group:AudienceGroup={id:"c0685b62-abdb-4696-aed0-6977d2ac8dc5",node_id:node,name:"West participants",role_ids:[role],include_descendants:true,active:true,revision:1};
const workspace:OrganizationWorkspace={nodes:[{id:node,name:"West",kind:"TERRITORY",active:true,revision:1}],roles:[{id:role,name:"Participants",capabilities:["organization.read"]}],staff:[{subject_id:participant,node_id:node,role_id:role,status:"ACTIVE",revision:1}]};
const editingSession:AdminSession={...adminSession,roles:["SUPER_ADMIN"]};
function show(session=editingSession,value=workspace){const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0},mutations:{retry:false}}});return render(<QueryClientProvider client={client}><AudienceWorkspace session={session} workspace={value} /></QueryClientProvider>);}
const problem=(status:number)=>Response.json({error:{code:"DENIED",message:"Request rejected",correlation_id:"test",retryable:false}},{status});

it("hides editing and membership controls from read-only staff",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(Response.json({groups:[group]}))));
 show({...adminSession,subject_id:participant,roles:["REGIONAL_STAFF"]});
 await userEvent.click(await screen.findByRole("button",{name:group.name}));
 expect(screen.getByText(/current staff-management assignment is required/)).toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Save audience group"})).not.toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Check current membership"})).not.toBeInTheDocument();
});

it("creates a scoped definition without caller identity or tenant authority",async()=>{
 let saved:AudienceGroup|undefined;let request:RequestInit|undefined;
 vi.stubGlobal("fetch",vi.fn((_path:string,init?:RequestInit)=>{if(init?.method==="POST"){request=init;const body=JSON.parse(init.body as string) as Omit<AudienceGroup,"id">;saved={...group,...body,revision:1};return Promise.resolve(Response.json(saved,{status:201}));}return Promise.resolve(Response.json({groups:saved?[saved]:[]}));}));
 show();await screen.findByRole("heading",{name:"New audience group"});
 await userEvent.type(screen.getByLabelText("Audience name"),group.name);await userEvent.click(screen.getByLabelText("Participants"));
 await userEvent.click(screen.getByRole("button",{name:"Save audience group"}));
 await screen.findByRole("button",{name:group.name});
 expect(JSON.parse(request?.body as string)).toEqual({node_id:node,name:group.name,role_ids:[role],include_descendants:true,active:true,revision:0});
 const headers=new Headers(request?.headers);expect(headers.get("X-CSRF-Token")).toBe(adminSession.csrf_token);expect(headers.has("Authorization")).toBe(false);
});

it("previews saved criteria and hides old results after local criteria change",async()=>{
 let previewBody:unknown;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{if(path.endsWith("/preview")){previewBody=JSON.parse(init?.body as string);return Promise.resolve(Response.json({member:true}));}return Promise.resolve(Response.json({groups:[group]}));}));
 show();await userEvent.click(await screen.findByRole("button",{name:group.name}));
 await userEvent.selectOptions(screen.getByLabelText("Participant account"),participant);
 await userEvent.click(screen.getByRole("button",{name:"Check current membership"}));
 await screen.findByText("This participant is currently a member.");expect(previewBody).toEqual({subject_id:participant});
 await userEvent.click(screen.getByLabelText("Include participants in subterritories"));
 expect(screen.queryByText("This participant is currently a member.")).not.toBeInTheDocument();expect(screen.getByRole("button",{name:"Check current membership"})).toBeDisabled();
});

it("preserves edits after a revision conflict and sends the original revision",async()=>{
 let body:unknown;
 vi.stubGlobal("fetch",vi.fn((_path:string,init?:RequestInit)=>{if(init?.method==="PUT"){body=JSON.parse(init.body as string);return Promise.resolve(problem(409));}return Promise.resolve(Response.json({groups:[group]}));}));
 show();await userEvent.click(await screen.findByRole("button",{name:group.name}));
 await userEvent.clear(screen.getByLabelText("Audience name"));await userEvent.type(screen.getByLabelText("Audience name"),"Updated audience");await userEvent.click(screen.getByRole("button",{name:"Save audience group"}));
 await screen.findByText("The group changed. Refresh before saving your edits.");expect(screen.getByLabelText("Audience name")).toHaveValue("Updated audience");expect(body).toMatchObject({revision:1,name:"Updated audience"});
});

it("requires fresh authentication for saved changes and membership lookup",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(Response.json({groups:[group]}))));
 show({...editingSession,assurance:{...adminSession.assurance,fresh_auth:false}});await userEvent.click(await screen.findByRole("button",{name:group.name}));
 expect(screen.getByRole("button",{name:"Save audience group"})).toBeDisabled();expect(screen.getByRole("button",{name:"Check current membership"})).toBeDisabled();expect(screen.getByRole("link")).toHaveAttribute("href","/login?reauth=mfa");
});

it("removes revoked groups and stops displaying the previous editor",async()=>{
 let revoked=false;
 vi.stubGlobal("fetch",vi.fn((_path:string,init?:RequestInit)=>{if(init?.method==="PUT"){revoked=true;return Promise.resolve(problem(403));}return Promise.resolve(Response.json({groups:revoked?[]:[group]}));}));
 show();await userEvent.click(await screen.findByRole("button",{name:group.name}));await userEvent.click(screen.getByRole("button",{name:"Save audience group"}));
 await screen.findByText("No audience groups are available in your current scope.");expect(screen.queryByRole("heading",{name:"Edit audience group"})).not.toBeInTheDocument();expect(screen.queryByDisplayValue(group.name)).not.toBeInTheDocument();
});

it("aborts an outgoing preview when its account/scope view is removed",async()=>{
 let signal:AbortSignal|undefined|null;let finish:((value:Response)=>void)|undefined;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{if(path.endsWith("/preview")){signal=init?.signal;return new Promise<Response>(resolve=>{finish=resolve;});}return Promise.resolve(Response.json({groups:[group]}));}));
 const shown=show();await userEvent.click(await screen.findByRole("button",{name:group.name}));await userEvent.selectOptions(screen.getByLabelText("Participant account"),participant);await userEvent.click(screen.getByRole("button",{name:"Check current membership"}));
 await waitFor(()=>{expect(signal).toBeDefined();});shown.unmount();expect(signal?.aborted).toBe(true);await act(async()=>{finish?.(Response.json({member:true}));await Promise.resolve();});expect(screen.queryByText("This participant is currently a member.")).not.toBeInTheDocument();
});

it("shows an unavailable state without reusing cached group data",async()=>{
 let unavailable=false;
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(unavailable?problem(503):Response.json({groups:[group]}))));
 show();await screen.findByRole("button",{name:group.name});unavailable=true;await userEvent.click(screen.getByRole("button",{name:"Refresh audience groups"}));
 await screen.findByRole("alert");expect(screen.queryByRole("button",{name:group.name})).not.toBeInTheDocument();
});

it("loads subsequent authorized pages without discarding earlier groups",async()=>{
 const second={...group,id:"d0685b62-abdb-4696-aed0-6977d2ac8dc5",name:"Second page group"};
 const paths:string[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string)=>{paths.push(path);return Promise.resolve(Response.json(path.includes("?after=")?{groups:[second]}:{groups:[group],next_cursor:group.id}));}));
 show();await screen.findByRole("button",{name:group.name});
 await userEvent.click(screen.getByRole("button",{name:"Load more audience groups"}));
 await screen.findByRole("button",{name:second.name});
 expect(screen.getByRole("button",{name:group.name})).toBeInTheDocument();
 expect(paths[1]).toBe(`/admin/api/v1/organization/audiences?after=${group.id}`);
 expect(screen.queryByRole("button",{name:"Load more audience groups"})).not.toBeInTheDocument();
});

it("hides the entire directory after a subsequent page loses access and permits recovery",async()=>{
 let denied=true;
 vi.stubGlobal("fetch",vi.fn((path:string)=>Promise.resolve(path.includes("?after=") && denied ? problem(403) : Response.json(path.includes("?after=")?{groups:[]}:{groups:[group],next_cursor:group.id}))));
 show();await screen.findByRole("button",{name:group.name});await userEvent.click(screen.getByRole("button",{name:"Load more audience groups"}));
 await screen.findByRole("alert");expect(screen.queryByRole("button",{name:group.name})).not.toBeInTheDocument();
 denied=false;await userEvent.click(screen.getByRole("button",{name:"Refresh audience groups"}));await screen.findByRole("button",{name:group.name});
});

it("finds a newly saved group beyond the current directory page",async()=>{
 const created={...group,id:"f0685b62-abdb-4696-aed0-6977d2ac8dc5",name:"New later group"};let saved=false;
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>{
  if(init?.method==="POST"){saved=true;return Promise.resolve(Response.json(created,{status:201}));}
  return Promise.resolve(Response.json(path.includes("?after=")?{groups:[created]}:{groups:[group],...(saved?{next_cursor:group.id}:{})}));
 }));show();await screen.findByRole("button",{name:group.name});
 await userEvent.type(screen.getByLabelText("Audience name"),created.name);await userEvent.click(screen.getByLabelText("Participants"));await userEvent.click(screen.getByRole("button",{name:"Save audience group"}));
 await screen.findByRole("heading",{name:"Edit audience group"});expect(screen.getByLabelText("Audience name")).toHaveValue(created.name);expect(screen.getByRole("button",{name:created.name})).toHaveAttribute("aria-pressed","true");
});
