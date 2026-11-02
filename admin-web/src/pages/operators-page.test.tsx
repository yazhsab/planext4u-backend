import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { OperatorsPage } from "./operators-page";
import { adminSession } from "../test/fixtures";
import type { AdminSession } from "../api/client";
import { t } from "../lib/i18n";
import { createOperator, getOperatorEvents, getOperators, updateOperator } from "../api/operators";

afterEach(() => {vi.unstubAllGlobals();});
const id = "00000000-0000-4000-8000-000000000111";
const account = {subject_id: id, provider_subject: "firebase-regional", display_name: "Regional operator", roles: ["REGIONAL_STAFF"], allowed_countries: ["IN"], active: true, revision: 1};
const event = {id, actor_id: id, country: "IN", action: "CREATED", revision: 1, roles: ["REGIONAL_STAFF"], allowed_countries: ["IN"], active: true, reason: "Approved regional onboarding", occurred_at: "2026-10-25T12:00:00Z"};
const superSession: AdminSession = {...adminSession, roles: ["SUPER_ADMIN"], subject_id: "00000000-0000-4000-8000-000000000222"};
function renderPage(session: AdminSession = superSession) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: false}}});
  return render(<QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<Outlet context={session} />}><Route index element={<OperatorsPage />} /></Route></Routes></MemoryRouter></QueryClientProvider>);
}

it("creates a scoped operator and edits persisted revision with CSRF, then displays audit history", async () => {
  let current = account;
  const sent: {url: string; init?: RequestInit}[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    sent.push({url, init});
    if (url.includes("/events?")) return Promise.resolve(Response.json({items: [event]}));
    if (init?.method === "POST") return Promise.resolve(Response.json(account, {status: 201}));
    if (init?.method === "PUT") {current = {...account, active: false, revision: 2}; return Promise.resolve(Response.json(current));}
    return Promise.resolve(Response.json({items: [current]}));
  }));
  renderPage(); const user = userEvent.setup();
  await screen.findByRole("heading", {name: "Operator accounts"});
  await user.type(screen.getByLabelText("Name"), "Regional operator");
  await user.type(screen.getByLabelText("Firebase user ID"), "firebase-regional");
  await user.type(screen.getByLabelText("Reason for access change"), "Approved regional onboarding");
  await user.click(screen.getByRole("button", {name: "Create operator"}));
  await screen.findByRole("status");
  const create = sent.find((value) => value.init?.method === "POST");
  if (typeof create?.init?.body !== "string") throw new Error("Missing create body");
  expect(JSON.parse(create.init.body)).toEqual({display_name: "Regional operator", provider_subject: "firebase-regional", roles: ["REGIONAL_STAFF"], allowed_countries: ["IN"], active: true, revision: 0, reason: "Approved regional onboarding"});
  expect(new Headers(create.init.headers).get("X-CSRF-Token")).toBe(superSession.csrf_token);
  await waitFor(() => {expect(screen.getByLabelText("Firebase user ID")).toHaveValue("");});
  await user.click(screen.getByRole("button", {name: "Edit access: Regional operator"}));
  expect(screen.queryByLabelText("Firebase user ID")).not.toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("Status"), "false");
  await user.click(screen.getByLabelText("Auditor"));
  await user.click(screen.getByLabelText("SG"));
  await user.type(screen.getByLabelText("Reason for access change"), "Approved access suspension");
  await user.click(screen.getByRole("button", {name: "Save access"}));
  await screen.findByText(/Regional staff · IN · Suspended/);
  const edit = sent.find((value) => value.init?.method === "PUT");
  if (typeof edit?.init?.body !== "string") throw new Error("Missing update body");
  expect(JSON.parse(edit.init.body)).toMatchObject({revision: 1, active: false, roles: ["AUDITOR", "REGIONAL_STAFF"], allowed_countries: ["IN", "SG"]});
  expect(JSON.parse(edit.init.body)).not.toHaveProperty("provider_subject");
  expect(JSON.parse(edit.init.body)).not.toHaveProperty("tenant_id");
  await user.click(screen.getByRole("button", {name: "Access history: Regional operator"}));
  await screen.findByText("Approved regional onboarding");
  await user.click(screen.getByRole("button", {name: "Close history"}));
  expect(screen.queryByText("Approved regional onboarding")).not.toBeInTheDocument();
});

it("searches and pages the directory and bounded history, and clears edits before refresh", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    requests.push(url);
    if (url.includes("/events?")) return Promise.resolve(Response.json(url.includes("before=0") ? {items: [event], next_before: 1} : {items: []}));
    return Promise.resolve(Response.json(url.includes("after=" + id) ? {items: []} : {items: [account], next_after: id}));
  }));
  renderPage(); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", {name: "Next page"}));
  await screen.findByText("No operator accounts in this country scope.");
  await user.click(screen.getByRole("button", {name: "First page"}));
  await screen.findByRole("button", {name: "Edit access: Regional operator"});
  await user.type(screen.getByLabelText("Search by name or account ID"), "Regional");
  await user.click(screen.getByRole("button", {name: "Search operators"}));
  await waitFor(() => {expect(requests.some((url) => url.includes("search=Regional"))).toBe(true);});
  await user.click(screen.getByRole("button", {name: "Edit access: Regional operator"}));
  await user.click(screen.getByRole("button", {name: "Refresh directory"}));
  expect(screen.getByLabelText("Firebase user ID")).toHaveValue("");
  await user.click(screen.getByRole("button", {name: "Access history: Regional operator"}));
  await user.click(await screen.findByRole("button", {name: "Older changes"}));
  await screen.findByText("No access changes recorded by this workflow.");
  await user.click(screen.getByRole("button", {name: "Latest changes"}));
  await screen.findByText("Approved regional onboarding");
  expect(requests.some((url) => url.endsWith("before=1"))).toBe(true);
});

it("keeps a stale form visible on conflict and shows provider failures without success", async () => {
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return Promise.resolve(Response.json({error: {code: "OPERATOR_CONFLICT", message: "Refresh before retrying", correlation_id: "corr-operator-conflict", retryable: false}}, {status: 409}));
    if (url.includes("/events?")) return Promise.resolve(Response.json({error: {code: "OPERATORS_UNAVAILABLE", message: "History unavailable", correlation_id: "corr-history", retryable: true}}, {status: 503}));
    return Promise.resolve(Response.json({items: [account]}));
  }));
  renderPage(); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", {name: "Edit access: Regional operator"}));
  await user.type(screen.getByLabelText("Reason for access change"), "Approved regional update");
  await user.click(screen.getByRole("button", {name: "Save access"}));
  await screen.findByText("Refresh before retrying");
  expect(screen.getByLabelText("Reason for access change")).toHaveValue("Approved regional update");
  expect(screen.queryByText(/Operator access saved/)).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", {name: "Access history: Regional operator"}));
  await screen.findByText("History unavailable");
  await user.click(screen.getByRole("button", {name: "Clear form"}));
  expect(screen.queryByText("Refresh before retrying")).not.toBeInTheDocument();
});

it("denies other roles before directory requests and disables stale-MFA changes", async () => {
  const fetch = vi.fn(() => Promise.resolve(Response.json({items: [{...account, roles: ["SUPER_ADMIN"]}]})));
  vi.stubGlobal("fetch", fetch);
  const view = renderPage({...superSession, roles: ["COUNTRY_ADMIN"]});
  expect(screen.getByRole("alert")).toHaveTextContent("Only a super administrator");
  expect(fetch).not.toHaveBeenCalled(); view.unmount();
  renderPage({...superSession, assurance: {...superSession.assurance, fresh_auth: false}});
  await screen.findByRole("heading", {name: "Operator directory"});
  expect(screen.queryByRole("button", {name: "Edit access: Regional operator"})).not.toBeInTheDocument();
  expect(screen.getByRole("button", {name: "Create operator"})).toBeDisabled();
  expect(screen.getByLabelText("Name")).toBeDisabled();
  expect(t("operatorsTitle", "ta")).not.toBe(t("operatorsTitle", "en"));
});

it("renders bounded directory errors with retry and rejects malformed contract data", async () => {
  let retry = false;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(retry ? Response.json({items: []}) : Response.json({error: {code: "OPERATORS_UNAVAILABLE", message: "Directory offline", correlation_id: "corr-directory", retryable: true}}, {status: 503}))));
  renderPage(); await screen.findByText("Directory offline"); retry = true;
  await userEvent.click(screen.getByRole("button", {name: "Try again"}));
  await screen.findByText("No operator accounts in this country scope.");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({items: [null]}))));
  await expect(getOperators("", "")).rejects.toThrow();
  expect(() => getOperatorEvents("invalid", 0)).toThrow();
  expect(() => updateOperator("invalid", {display_name: "Name", roles: ["AUDITOR"], allowed_countries: ["IN"], active: true, revision: 1, reason: "Valid reason"}, "csrf")).toThrow();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json(account))));
  await expect(createOperator({display_name: "Name", provider_subject: "uid", roles: ["AUDITOR"], allowed_countries: ["IN"], active: true, revision: 0, reason: "Valid reason"}, "csrf")).resolves.toMatchObject({subject_id: id});
  const directory = screen.getByRole("heading", {name: "Operator directory"}).parentElement;
  if (!directory) throw new Error("Missing directory section");
  expect(within(directory).getByText("No operator accounts in this country scope.")).toBeInTheDocument();
});
