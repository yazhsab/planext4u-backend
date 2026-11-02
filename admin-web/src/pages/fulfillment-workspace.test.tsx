import {QueryClient, QueryClientProvider} from "@tanstack/react-query";
import {render,screen,within,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {FulfillmentWorkspace} from "./fulfillment-workspace";
import {adminSession} from "../test/fixtures";

afterEach(() => {vi.unstubAllGlobals();});
const territories = {items: [{id: "00000000-0000-4000-8000-000000000001",name: "North deliveries",region_id: "NORTH"},{id: "00000000-0000-4000-8000-000000000002",name: "South deliveries",region_id: "SOUTH"}]};
const dashboard = {region_id: "NORTH",territories: 1,online_riders: 2,unassigned_tasks: 3,at_risk_tasks: 0,completed_today: 4,generated_at: "2026-10-25T09:00:00Z"};
function show() {return render(<QueryClientProvider client={new QueryClient({defaultOptions: {queries: {retry: false}}})}><FulfillmentWorkspace session={adminSession} /></QueryClientProvider>);}

it("loads current delivery resources and regions without submitting authority",async () => {
  const paths: string[] = [];
  vi.stubGlobal("fetch",vi.fn((url: string) => {paths.push(url);return Promise.resolve(Response.json(url.endsWith("territories") ? territories : {...dashboard,region_id: url.endsWith("SOUTH") ? "SOUTH" : "NORTH"}));}));
  show();
  await screen.findByText("North deliveries · NORTH");
  const list = await screen.findByText("Online riders");
  const metric = list.parentElement;
  if (!metric) throw new Error("Missing operational metric group");
  expect(within(metric).getByRole("definition")).toHaveTextContent("2");
  await userEvent.selectOptions(screen.getByLabelText("Delivery region"),"SOUTH");
  await waitFor(() => {expect(paths).toContain("/admin/api/v1/fulfillment/regions/SOUTH");});
  expect(paths.every((path) => !path.includes("?") && !path.includes("tenant"))).toBe(true);
});

it("removes cached operational totals after an access denial",async () => {
  let denied = false;
  vi.stubGlobal("fetch",vi.fn((url: string) => Promise.resolve(denied ? Response.json({error: {code: "ADMIN_FULFILLMENT_FORBIDDEN",message: "Revoked",correlation_id: "revoked",retryable: false}},{status: 403}) : Response.json(url.endsWith("territories") ? territories : dashboard))));
  show(); await screen.findByText("Online riders");
  denied = true;
  await userEvent.click(screen.getByRole("button",{name: "Refresh delivery operations"}));
  await screen.findByRole("alert");
  expect(screen.queryByText("Online riders")).not.toBeInTheDocument();
  expect(screen.queryByText("North deliveries · NORTH")).not.toBeInTheDocument();
});

it("shows no assigned territories without fabricating operational totals",async () => {
  const fetch = vi.fn(() => Promise.resolve(Response.json({items: []})));
  vi.stubGlobal("fetch",fetch);show();
  await screen.findByText("No delivery territories are available under your current assignments.");
  expect(screen.queryByText("Online riders")).not.toBeInTheDocument();
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("rejects incomplete owning-service counters and offers a visible refresh",async () => {
  vi.stubGlobal("fetch",vi.fn((url: string) => Promise.resolve(Response.json(url.endsWith("territories") ? territories : {region_id: "NORTH"}))));
  show();await screen.findByRole("alert");
  expect(screen.queryByText("Online riders")).not.toBeInTheDocument();
  expect(screen.getByRole("button",{name: "Refresh delivery operations"})).toBeEnabled();
});

it("hides field controls when the current server assignment grants reads only",async()=>{
  vi.stubGlobal("fetch",vi.fn((url:string)=>Promise.resolve(Response.json(url.endsWith("field-territories")?{territory_ids:[]}:url.endsWith("territories")?territories:dashboard))));
  render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><FulfillmentWorkspace session={{...adminSession,capabilities:[...adminSession.capabilities,"admin.fulfillment.command"]}} /></QueryClientProvider>);
  await screen.findByText("Online riders");
  expect(screen.queryByRole("button",{name:"Use current location"})).not.toBeInTheDocument();
});

it("removes cached field controls after command permission is revoked while reads remain",async()=>{
  let command=true;
  vi.stubGlobal("fetch",vi.fn((url:string)=>Promise.resolve(Response.json(url.endsWith("field-territories")?{territory_ids:command?[territories.items[0]?.id]:[]}:url.endsWith("territories")?territories:dashboard))));
  render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><FulfillmentWorkspace session={{...adminSession,capabilities:[...adminSession.capabilities,"admin.fulfillment.command"]}} /></QueryClientProvider>);
  await screen.findByRole("button",{name:"Use current location"});command=false;
  await userEvent.click(screen.getByRole("button",{name:"Refresh delivery operations"}));
  await waitFor(()=>{expect(screen.queryByRole("button",{name:"Use current location"})).not.toBeInTheDocument();});
  expect(screen.getByText("Online riders")).toBeInTheDocument();
});
