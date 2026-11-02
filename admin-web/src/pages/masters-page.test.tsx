import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { MastersPage } from "./masters-page";
import { adminSession } from "../test/fixtures";
import type { AdminSession } from "../api/client";
import { getMasterHistory, getMasters, saveMaster } from "../api/masters";
import { t } from "../lib/i18n";

afterEach(() => {vi.unstubAllGlobals(); vi.restoreAllMocks();});
const id = "00000000-0000-4000-8000-000000000123";
const parentID = "00000000-0000-4000-8000-000000000124";
const entry = {id, kind: "OCCUPATION" as const, code: "TEACHER", name_en: "Teacher", name_ta: "ஆசிரியர்", active: true, revision: 1};
const event = {definition: entry, actor_id: id, reason: "Approved initial occupation", occurred_at: "2026-10-26T10:00:00Z"};
const bodyText = (value: RequestInit["body"]) => typeof value === "string" ? value : "";
function renderPage(session: AdminSession = adminSession) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: false}}});
  return render(<QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<Outlet context={session} />}><Route index element={<MastersPage />} /></Route></Routes></MemoryRouter></QueryClientProvider>);
}
it("creates and revises bilingual entries using current revision and CSRF then reads history", async () => {
  const sent: {url: string; init?: RequestInit}[] = []; let current = entry;
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    sent.push({url, init});
    if (url.includes("/history?")) return Promise.resolve(Response.json({items: [event]}));
    if (init?.method === "PUT") {const body = JSON.parse(bodyText(init.body)) as typeof entry; current = {...body, revision: body.revision + 1}; return Promise.resolve(Response.json(current));}
    return Promise.resolve(Response.json({items: [current]}));
  }));
  renderPage(); const user = userEvent.setup(); await screen.findByText("Teacher", {selector: "strong"});
  await user.type(screen.getByLabelText("Stable code"), "ENGINEER"); await user.type(screen.getByLabelText("English name"), "Engineer"); await user.type(screen.getByLabelText("Tamil name"), "பொறியாளர்"); await user.type(screen.getByLabelText("Reason for change"), "Approved new occupation");
  await user.click(screen.getByRole("button", {name: "Save master data"})); await screen.findByText("Master data saved with its change history.");
  const create = sent.find((item) => item.init?.method === "PUT"); expect(new Headers(create?.init?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token); expect(JSON.parse(bodyText(create?.init?.body))).toMatchObject({kind: "OCCUPATION", code: "ENGINEER", revision: 0, name_ta: "பொறியாளர்"});
  await user.click(await screen.findByRole("button", {name: "Edit entry: Engineer"})); expect(screen.getByLabelText("Stable code")).toHaveAttribute("readonly");
  await user.selectOptions(screen.getByLabelText("Entry status"), "false"); await user.type(screen.getByLabelText("Reason for change"), "Deactivate old occupation"); await user.click(screen.getByRole("button", {name: "Save master data"}));
  await waitFor(() => {expect(sent.filter((item) => item.init?.method === "PUT")).toHaveLength(2);}); expect(JSON.parse(bodyText(sent.filter((item) => item.init?.method === "PUT")[1]?.init?.body))).toMatchObject({revision: 1, active: false});
  await user.click(await screen.findByRole("button", {name: "Change history: Engineer"})); await screen.findByText("Approved initial occupation"); await user.click(screen.getByRole("button", {name: "Close history"}));
});
it("paginates literal search/status and history, refreshes a stale form and exposes failures", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    requests.push(url);
    if (init?.method === "PUT") return Promise.resolve(Response.json({error: {code: "ORGANIZATION_CONFLICT", message: "Refresh before retrying", correlation_id: "corr-master-conflict", retryable: false}}, {status: 409}));
    if (url.includes("/history?")) return Promise.resolve(Response.json(url.endsWith("before=0") ? {items: [event], next_before: 1} : {items: []}));
    return Promise.resolve(Response.json(url.includes("after=" + id) ? {items: []} : {items: [entry], next_after: id}));
  }));
  renderPage(); const user = userEvent.setup(); await user.click(await screen.findByRole("button", {name: "Next page"})); await screen.findByText("No matching master data."); await user.click(screen.getByRole("button", {name: "First page"}));
  await user.type(screen.getByLabelText("Search names or code"), "Teacher%"); await user.selectOptions(screen.getByLabelText("Status", {exact: true}), "ACTIVE"); await user.click(screen.getByRole("button", {name: "Search master data"}));
  await waitFor(() => {expect(requests.some((url) => url.includes("Teacher%25") && url.includes("status=ACTIVE"))).toBe(true);});
  await user.click(await screen.findByRole("button", {name: "Edit entry: Teacher"})); await user.type(screen.getByLabelText("Reason for change"), "Update current occupation"); await user.click(screen.getByRole("button", {name: "Save master data"})); await screen.findByText("Refresh before retrying"); expect(screen.getByLabelText("English name")).toHaveValue("Teacher");
  await user.click(screen.getByRole("button", {name: "Refresh directory"})); expect(screen.getByLabelText("English name")).toHaveValue("");
  await user.click(screen.getByRole("button", {name: "Change history: Teacher"})); await user.click(await screen.findByRole("button", {name: "Older changes"})); await screen.findByText("No access changes recorded by this workflow."); await user.click(screen.getByRole("button", {name: "Latest changes"})); await screen.findByText("Approved initial occupation");
  await user.click(screen.getByRole("button", {name: "Clear form"}));
});
it("requires a current parent for geography and preserves immutable parent on edits", async () => {
  const sent: string[] = []; const payloads: unknown[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    sent.push(url);
    if (init?.method === "PUT") {const payload = JSON.parse(bodyText(init.body)) as typeof entry; payloads.push(payload); return Promise.resolve(Response.json({...payload, revision: 1}));}
    if (url.includes("kind=COUNTRY")) return Promise.resolve(Response.json({items: [{...entry, id: parentID, kind: "COUNTRY", code: "IN", name_en: "India"}], next_after: parentID}));
    if (url.includes("kind=STATE")) return Promise.resolve(Response.json({items: []}));
    return Promise.resolve(Response.json({items: [entry]}));
  }));
  renderPage(); const user = userEvent.setup(); await user.selectOptions(screen.getByLabelText("Master type"), "STATE");
  await screen.findByRole("option", {name: "India · IN"}); await user.type(screen.getByLabelText("Search parent names or code"), "India");
  await waitFor(() => {expect(sent.some((url) => url.includes("kind=COUNTRY") && url.includes("search=India"))).toBe(true);});
  await user.click(await screen.findByRole("button", {name: "Next parent page"})); await screen.findByRole("button", {name: "First parent page"}); await user.click(screen.getByRole("button", {name: "First parent page"}));
  await user.selectOptions(screen.getByLabelText("Parent entry Country"), parentID); await user.type(screen.getByLabelText("Stable code"), "TN"); await user.type(screen.getByLabelText("English name"), "Tamil Nadu"); await user.type(screen.getByLabelText("Tamil name"), "தமிழ்நாடு"); await user.type(screen.getByLabelText("Reason for change"), "Approve initial state"); await user.click(screen.getByRole("button", {name: "Save master data"}));
  await waitFor(() => {expect(payloads[0]).toMatchObject({kind: "STATE", parent_id: parentID, code: "TN"});});
  await user.selectOptions(screen.getByLabelText("Master type"), "COUNTRY"); expect(screen.getByLabelText("Stable code")).toHaveValue("IN"); expect(screen.getByLabelText("Stable code")).toHaveAttribute("readonly");
});
it("denies regional access, gives auditors read-only history and disables stale edits", async () => {
  const fetcher = vi.fn(() => Promise.resolve(Response.json({items: [entry]}))); vi.stubGlobal("fetch", fetcher);
  const view = renderPage({...adminSession, roles: ["REGIONAL_STAFF"]}); expect(screen.getByRole("alert")).toHaveTextContent("Your role cannot access"); expect(fetcher).not.toHaveBeenCalled(); view.unmount();
  const auditor = renderPage({...adminSession, roles: ["AUDITOR"]}); await screen.findByRole("button", {name: "Change history: Teacher"}); expect(screen.queryByRole("button", {name: "Save master data"})).not.toBeInTheDocument(); expect(screen.queryByRole("button", {name: "Edit entry: Teacher"})).not.toBeInTheDocument(); auditor.unmount();
  renderPage({...adminSession, assurance: {...adminSession.assurance, fresh_auth: false}}); expect(screen.getByRole("button", {name: "Save master data"})).toBeDisabled(); expect(screen.getByText("Master data changes require a fresh multi-factor sign-in.")).toBeInTheDocument();
});
it("shows owner read failures and permits a normal retry without showing stale success", async () => {
  let failed = true; vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(failed ? Response.json({error: {code: "ORGANIZATION_UNAVAILABLE", message: "Owner unavailable", correlation_id: "corr-master-owner", retryable: true}}, {status: 503}) : Response.json({items: []}))));
  renderPage(); const alert = await screen.findByRole("alert"); expect(alert).toHaveTextContent("Owner unavailable"); failed = false; await userEvent.click(within(alert).getByRole("button", {name: "Try again"})); await screen.findByText("No matching master data."); expect(screen.queryByText("Master data saved with its change history.")).not.toBeInTheDocument();
});
it("rejects invalid owner payloads and identifiers and provides Tamil labels", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({items: [{...entry, id: "invalid"}]}))));
  await expect(getMasters("OCCUPATION", "", "ALL", "")).rejects.toThrow();
  expect(() => saveMaster({...entry, id: "invalid", reason: "Approved master"}, "csrf")).toThrow(); expect(() => getMasterHistory("invalid", 0)).toThrow();
  expect(t("mastersTitle", "ta-IN")).toBe("அடிப்படைத் தரவுகள்");
});
