import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { OrganizationPage } from "./organization-page";
import { LoginPage } from "./login-page";
import { adminSession } from "../test/fixtures";
import { t } from "../lib/i18n";

afterEach(() => {vi.unstubAllGlobals(); vi.unstubAllEnvs();});
it("shows delegated delivery resources even when organization-tree access is not assigned",async () => {
  vi.stubGlobal("fetch",vi.fn((url: string) => Promise.resolve(url.includes("/fulfillment/") ? Response.json({items: []}) : Response.json({error:{code:"ORGANIZATION_FORBIDDEN",message:"Organization tree unavailable",correlation_id:"org-resource-only",retryable:false}},{status:403}))));
  renderOrganization({...adminSession,roles:["REGIONAL_STAFF"],capabilities:["admin.shell.read","admin.fulfillment.read"]});
  await screen.findByRole("heading",{name:"Delivery operations"});
  await screen.findByText("No delivery territories are available under your current assignments.");
  expect(screen.queryByRole("heading",{name:"Fulfillment territory access"})).not.toBeInTheDocument();
});
const nodeID = "00000000-0000-4000-8000-000000000010";
const roleID = "00000000-0000-4000-8000-000000000020";
const subject = "00000000-0000-4000-8000-000000000030";
const node = {id: nodeID, name: "North territory", kind: "TERRITORY", active: true, revision: 1};
const role = {id: roleID, name: "Viewer", capabilities: ["organization.read"]};
const staff = {subject_id: subject, node_id: nodeID, role_id: roleID, status: "ACTIVE", revision: 1};
const workspace = {nodes: [node], roles: [role], staff: [staff]};
function renderOrganization(session = adminSession) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: false}}});
  return render(<QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<Outlet context={session} />}><Route index element={<OrganizationPage />} /></Route></Routes></MemoryRouter></QueryClientProvider>);
}

it("accepts an intended invitation even before workspace access and clears the token", async () => {
  const requests: {url: string; init?: RequestInit}[] = [];
  let accepted = false;
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    requests.push({url, init});
    if (url.endsWith("/invitations/accept")) { accepted = true; return Promise.resolve(new Response(null, {status: 204})); }
    return Promise.resolve(accepted ? Response.json(workspace) : Response.json({error: {code: "ORGANIZATION_FORBIDDEN", message: "No assignment yet", correlation_id: "corr-invite", retryable: false}}, {status: 403}));
  }));
  renderOrganization();
  await screen.findByText("No assignment yet");
  const user = userEvent.setup();
  const token = screen.getByLabelText("Invitation token");
  await user.type(token, "a".repeat(43));
  await user.click(screen.getByRole("button", {name: "Accept invitation"}));
  await screen.findByRole("heading", {name: "Organization and staff"});
  expect(screen.getByLabelText("Invitation token")).toHaveValue("");
  const sent = requests.find((item) => item.url.endsWith("/invitations/accept"));
  expect(sent?.init?.body).toBe(JSON.stringify({token: "a".repeat(43)}));
  expect(new Headers(sent?.init?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
});

it("updates territory status using its current revision without granting client authority", async () => {
  let changed = false;
  let body: unknown;
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    if (url.endsWith("/territory-bindings")) return Promise.resolve(Response.json({items: []}));
    if (url.endsWith(`/nodes/${nodeID}/status`)) {
      if (typeof init?.body !== "string") throw new Error("Missing status mutation body");
      body = JSON.parse(init.body) as unknown;
      expect(init.method).toBe("PUT");
      expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
      changed = true;
      return Promise.resolve(new Response(null, {status: 204}));
    }
    return Promise.resolve(Response.json({...workspace, nodes: [{...node, active: !changed, revision: changed ? 2 : 1}]}));
  }));
  renderOrganization({...adminSession, roles: ["SUPER_ADMIN"]});
  await userEvent.click(await screen.findByRole("button", {name: "Deactivate territory"}));
  await screen.findByRole("button", {name: "Activate territory"});
  expect(body).toEqual({revision: 1, active: false});
});

it("links real delivery territories using the current server revision, reason and CSRF", async () => {
  const territoryID = "00000000-0000-4000-8000-000000000040";
  let sent: RequestInit | undefined;
  let revision = 0;
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    if (url.endsWith(`/territory-bindings/${territoryID}`)) {
      sent = init; revision = 1; return Promise.resolve(new Response(null, {status: 204}));
    }
    if (url.endsWith("/territory-bindings")) return Promise.resolve(Response.json({items: [{territory_id: territoryID, name: "Delivery North", region_id: "NORTH", revision, ...(revision ? {node_id: nodeID} : {})}]}));
    return Promise.resolve(Response.json(workspace));
  }));
  renderOrganization({...adminSession, roles: ["SUPER_ADMIN"]});
  const user = userEvent.setup();
  await user.selectOptions(await screen.findByLabelText("Organization territory for Delivery North"), nodeID);
  await user.type(screen.getByLabelText("Reason for linking Delivery North"), "Approved local territory boundary");
  await user.click(screen.getByRole("button", {name: "Save territory link"}));
  await screen.findByText("Territory access link saved.");
  expect(sent?.method).toBe("PUT");
  expect(new Headers(sent?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
  expect(JSON.parse(sent?.body as string)).toEqual({node_id: nodeID, revision: 0, reason: "Approved local territory boundary"});
  await waitFor(() => {expect(screen.getByLabelText("Organization territory for Delivery North")).toHaveValue(nodeID);});
});

it("keeps territory binding controls hidden from delegated and country staff", async () => {
  const fetch = vi.fn((url:string) => Promise.resolve(Response.json(url.endsWith("/audiences") ? {groups:[]} : workspace)));
  vi.stubGlobal("fetch", fetch);
  renderOrganization();
  await screen.findByRole("heading", {name: "Organization and staff"});
  expect(screen.queryByRole("heading", {name: "Fulfillment territory access"})).not.toBeInTheDocument();
  expect(fetch.mock.calls.some(([url])=>url.endsWith("/territory-bindings"))).toBe(false);
});

it("manages only the current server workspace and uses CSRF for staff changes", async () => {
  const requests: {url: string; init?: RequestInit}[] = [];
  const fetch = vi.fn((url: string, init?: RequestInit) => {
    requests.push({url, init});
    if (url.endsWith("/territory-bindings")) return Promise.resolve(Response.json({items: []}));
    if (url.endsWith("/nodes")) return Promise.resolve(Response.json({...node, name: "New territory"}, {status: 201}));
    if (url.endsWith("/roles")) return Promise.resolve(Response.json(role, {status: 201}));
    if (url.endsWith("/staff")) return Promise.resolve(Response.json({...staff, status: "SUSPENDED", revision: 2}));
    if (url.endsWith("/invitations")) return Promise.resolve(Response.json({id: nodeID, token: "a".repeat(43), subject_id: subject, node_id: nodeID, role_id: roleID, expires_at: "2026-10-26T09:00:00Z"}, {status: 201}));
    return Promise.resolve(Response.json(workspace));
  }); vi.stubGlobal("fetch", fetch);
  renderOrganization({...adminSession,roles:["SUPER_ADMIN"]});
  await screen.findByRole("heading", {name: "Organization and staff"});
  const user = userEvent.setup();
  const tree = screen.getByRole("heading", {name: "Organization tree"}).parentElement;
  if (!tree) throw new Error("Missing organization tree");
  await user.type(within(tree).getByLabelText("Name"), "New territory");
  await user.selectOptions(within(tree).getByLabelText("Parent"), nodeID);
  await user.click(within(tree).getByRole("button", {name: "Create"}));
  await waitFor(() => {expect(requests.some((item) => item.url.endsWith("/nodes"))).toBe(true);});
  await user.click(screen.getByRole("button", {name: "Suspend"}));
  await waitFor(() => {expect(requests.some((item) => item.url.endsWith("/staff"))).toBe(true);});
  const roles = screen.getByRole("heading", {name: "Job roles"}).parentElement;
  if (!roles) throw new Error("Missing job roles");
  await user.type(within(roles).getByLabelText("Name"), "Viewer");
  await user.selectOptions(within(roles).getByLabelText("Territory"), nodeID);
  await user.click(within(roles).getByLabelText("View organization"));
  await user.click(within(roles).getByRole("button", {name: "Create"}));
  const staffSection = screen.getByRole("heading", {name: "Staff assignments"}).parentElement;
  if (!staffSection) throw new Error("Missing staff section");
  await user.type(within(staffSection).getByLabelText("Account ID"), subject);
  await user.selectOptions(within(staffSection).getByLabelText("Territory"), nodeID);
  await user.selectOptions(within(staffSection).getByLabelText("Job roles"), roleID);
  await user.click(within(staffSection).getByRole("button", {name: "Create invitation"}));
  await screen.findByText(/Share this single-use token/);
  for (const item of requests.filter((item) => item.init?.method !== undefined)) {
    expect(new Headers(item.init?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
    if (typeof item.init?.body !== "string") throw new Error("Missing mutation body");
    const body: unknown = JSON.parse(item.init.body);
    expect(body).not.toHaveProperty("tenant_id"); expect(body).not.toHaveProperty("platform_admin");
  }
});

it("shows authorization failure and unconfigured sign-in without fabricating success", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({error: {code: "ORGANIZATION_FORBIDDEN", message: "Scope denied", correlation_id: "corr-denied", retryable: false}}, {status: 403}))));
  const view = renderOrganization();
  await screen.findByText("Scope denied");
  view.unmount();
  vi.stubEnv("VITE_FIREBASE_PROJECT_ID", "");
  render(<LoginPage />);
  expect(screen.getByRole("status")).toHaveTextContent("not configured");
  expect(screen.getByRole("button", {name: "Administrator sign in"})).toBeDisabled();
  expect(t("organizationTitle", "ta")).not.toBe(t("organizationTitle", "en"));
});

it("keeps management forms and staff commands out of a read-only assigned workspace",async () => {
  vi.stubGlobal("fetch",vi.fn(() => Promise.resolve(Response.json(workspace))));
  renderOrganization({...adminSession,subject_id:subject,roles:["REGIONAL_STAFF"]});
  await screen.findByRole("heading",{name:"Organization and staff"});
  expect(screen.queryByRole("button",{name:"Create"})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Create invitation"})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Suspend"})).not.toBeInTheDocument();
  expect(screen.getByRole("button",{name:"Accept invitation"})).toBeEnabled();
});

it("presents inherited management only inside active assigned nodes and filters delegated permissions",async () => {
  const child = {...node,id:"00000000-0000-4000-8000-000000000011",parent_id:nodeID,name:"North child"};
  const manager = {...role,capabilities:["organization.read","organization.manage","staff.manage"]};
  vi.stubGlobal("fetch",vi.fn(() => Promise.resolve(Response.json({...workspace,nodes:[node,child],roles:[manager]}))));
  renderOrganization({...adminSession,subject_id:subject,roles:["REGIONAL_STAFF"]});
  await screen.findByRole("heading",{name:"Organization and staff"});
  const parent = screen.getByLabelText("Parent");
  expect(within(parent).queryByRole("option",{name:"Country root"})).not.toBeInTheDocument();
  expect(within(parent).getByRole("option",{name:"North child"})).toBeInTheDocument();
  const roleSection = screen.getByRole("heading",{name:"Job roles"}).parentElement;
  if (!roleSection) throw new Error("Job role section missing");
  await userEvent.selectOptions(within(roleSection).getByLabelText("Territory"),child.id);
  expect(within(roleSection).getByLabelText("Manage staff")).toBeInTheDocument();
  expect(within(roleSection).queryByLabelText("Change assigned records")).not.toBeInTheDocument();
});

it("hides delegated management after a suspended assignment or inactive ancestor",async () => {
  const child = {...node,id:"00000000-0000-4000-8000-000000000011",parent_id:nodeID,name:"North child"};
  const manager = {...role,capabilities:["organization.read","organization.manage","staff.manage"]};
  vi.stubGlobal("fetch",vi.fn(() => Promise.resolve(Response.json({...workspace,nodes:[{...node,active:false},child],roles:[manager],staff:[{...staff,node_id:child.id},{...staff,status:"SUSPENDED"}]}))));
  renderOrganization({...adminSession,subject_id:subject,roles:["REGIONAL_STAFF"]});
  await screen.findByRole("heading",{name:"Organization and staff"});
  expect(screen.queryByRole("button",{name:"Create"})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Create invitation"})).not.toBeInTheDocument();
});
