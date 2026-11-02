import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { ParticipantsPage } from "./participants-page";
import { adminSession } from "../test/fixtures";
import type { AdminSession } from "../api/client";
import { getParticipants, rejectParticipant, type Participant } from "../api/participants";
import { t } from "../lib/i18n";

afterEach(() => {vi.unstubAllGlobals(); vi.restoreAllMocks();});
const current: AdminSession = {...adminSession, roles: ["COUNTRY_ADMIN"], capabilities: [...adminSession.capabilities, "admin.participants.review"]};
const vendor: Participant = {id: "30000000-0000-4000-8000-000000000001", participant_id: "20000000-0000-4000-8000-000000000001", kind: "VENDOR", revision: 2, status: "KYC_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", document_count: 2, bank_status: "", actions: ["REJECT"], updated_at: "2026-10-27T07:00:00Z"};
const receipt = {participant_id: vendor.participant_id, kind: "VENDOR", revision: 3, status: "REJECTED"};
const problem = (status: number) => Response.json({error: {code: "REVIEW_DENIED", message: "Current application is unavailable.", correlation_id: "synthetic", retryable: status >= 500}}, {status});
function renderPage(session = current) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: false}}});
  const element = (context: AdminSession) => <QueryClientProvider client={client}><MemoryRouter><Routes><Route element={<Outlet context={context} />}><Route index element={<ParticipantsPage />} /></Route></Routes></MemoryRouter></QueryClientProvider>;
  const result = render(element(session)); return {...result, changeSession: (next: AdminSession) => {result.rerender(element(next));}};
}
const selectRejection = async () => {const user = userEvent.setup(); await user.click(await screen.findByRole("button", {name: "Review rejection: Synthetic vendor"})); await user.type(screen.getByLabelText("Reason for change"), "Documented application rejection reason"); return user;};

it("keeps country queue and management controls outside other roles", () => {
  const fetch = vi.fn(); vi.stubGlobal("fetch", fetch); renderPage({...adminSession, roles: ["REGIONAL_STAFF"], capabilities: []});
  expect(screen.getByRole("alert")).toHaveTextContent("cannot review"); expect(fetch).not.toHaveBeenCalled();
});
it("shows a scoped read-only regional vendor detail and drops it after revocation", async () => {
  const regional = {...current, roles: ["REGIONAL_STAFF"] as AdminSession["roles"]};
  const detail = {id: vendor.id, participant_id: vendor.participant_id, kind: "VENDOR", revision: 2, status: "KYC_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", field_visit: {scheduled_at: "2026-10-27T06:00:00Z"}, service_zone_count: 1, updated_at: "2026-10-27T07:00:00Z"};
  const regionalItem = {id: vendor.id, participant_id: vendor.participant_id, kind: "VENDOR", revision: 2, status: "KYC_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", actions: ["REJECT"], updated_at: "2026-10-27T07:00:00Z"};
  let revoked = false; const requested: string[] = [];
  vi.stubGlobal("fetch", vi.fn((path: string) => {
    requested.push(path);
    if (path.endsWith("/review")) return Promise.resolve(revoked ? problem(403) : Response.json(detail));
    return Promise.resolve(Response.json({items: [regionalItem]}));
  }));
  const page = renderPage(regional); await screen.findByText("Synthetic vendor");
  expect(screen.queryByText(/Submitted documents:/)).not.toBeInTheDocument();
  expect(screen.queryByText(/Bank verification:/)).not.toBeInTheDocument();
  const user = userEvent.setup(); await user.click(screen.getByRole("button", {name: "Review application: Synthetic vendor"}));
  expect(await screen.findByRole("note")).toHaveTextContent("Read-only regional summary");
  expect(screen.getByText(/Service zones: 1/)).toBeInTheDocument();
  expect(screen.queryByText(/Document verification status/)).not.toBeInTheDocument();
  expect(screen.queryByText(/Synthetic contact/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", {name: "Approve application"})).not.toBeInTheDocument();
  expect(screen.queryByRole("button", {name: /Open private document/})).not.toBeInTheDocument();
  expect(requested.some((path) => path.endsWith(`/VENDOR/${vendor.participant_id}/review`))).toBe(true);
  revoked = true; page.changeSession({...regional, assurance: {...regional.assurance, auth_time: "2026-10-27T08:00:00Z"}});
  await screen.findByText("Current application is unavailable.");
  expect(screen.queryByRole("note")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", {name: "Approve application"})).not.toBeInTheDocument();
});
it("loads safe summaries and sends literal filters and kind without caller authority", async () => {
  const sent: string[] = []; vi.stubGlobal("fetch", vi.fn((url: string) => {sent.push(url); return Promise.resolve(Response.json({items: url.includes("/RIDER?") ? [] : [vendor]}));}));
  renderPage(); expect(await screen.findByText("Synthetic vendor")).toBeInTheDocument(); expect(screen.getByText("Submitted documents: 2 · Bank verification: Not submitted")).toBeInTheDocument();
  const user = userEvent.setup(); await user.type(screen.getByLabelText("Search applicant name"), "%_வீடு"); await user.selectOptions(screen.getByLabelText("Status"), "KYC_REVIEW"); await user.click(screen.getByRole("button", {name: "Find applications"}));
  await waitFor(() => {expect(new URL(sent.at(-1) ?? "", "https://local.test").searchParams.get("search")).toBe("%_வீடு");});
  expect(new URL(sent.at(-1) ?? "", "https://local.test").searchParams.get("status")).toBe("KYC_REVIEW");
  await user.selectOptions(screen.getByLabelText("Participant type"), "RIDER"); await screen.findByText("No matching applications."); expect(sent.some((url) => url.includes("/RIDER?"))).toBe(true); expect(sent.join(" ")).not.toMatch(/tenant|subject|country=/);
});
it("rejects country-only fields in a regional participant queue", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({items: [vendor]}))));
  await expect(getParticipants("VENDOR", "", "", "", undefined, true)).rejects.toThrow();
});
it("records a reasoned CAS command with current CSRF then reads authoritative state", async () => {
  const sent: RequestInit[] = []; let saved = false;
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {sent.push(init); saved = true; return Promise.resolve(Response.json(receipt));}
    return Promise.resolve(Response.json({items: [{...vendor, ...(saved ? {status: "REJECTED", revision: 3, actions: []} : {})}]}));
  })); renderPage(); const user = await selectRejection(); await user.click(screen.getByRole("button", {name: "Confirm rejection"}));
  await screen.findByText("Decision recorded: REJECTED (3)"); expect(await screen.findByText(/REJECTED · SOLE_PROPRIETOR/)).toBeInTheDocument();
  const headers = new Headers(sent[0]?.headers); expect(headers.get("X-CSRF-Token")).toBe(current.csrf_token); expect(headers.get("If-Match")).toBe('"2"'); expect(headers.get("Idempotency-Key")).toMatch(/^[a-f0-9-]{36}$/); expect(sent[0]?.body).toBe(JSON.stringify({reason: "Documented application rejection reason"}));
});
it("retains the same command after response loss and preserves acknowledged success when refresh fails", async () => {
  const keys: string[] = []; let posts = 0;
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {posts++; keys.push(new Headers(init.headers).get("Idempotency-Key") ?? ""); return posts === 1 ? Promise.reject(new TypeError("Response lost")) : Promise.resolve(Response.json(receipt));}
    return Promise.resolve(posts > 1 ? problem(503) : Response.json({items: [vendor]}));
  })); renderPage(); const user = await selectRejection(); await user.click(screen.getByRole("button", {name: "Confirm rejection"}));
  await screen.findByText(/The result is unconfirmed/); expect(screen.getByLabelText("Reason for change")).toHaveAttribute("readonly"); expect(screen.getByRole("button", {name: "Confirm rejection"})).toBeDisabled();
  await user.click(screen.getByRole("button", {name: "Retry same decision"})); await screen.findByText("Decision recorded: REJECTED (3)"); expect(keys).toHaveLength(2); expect(keys[1]).toBe(keys[0]); await screen.findByRole("button", {name: "Try again"}); expect(screen.queryByText("Synthetic vendor")).not.toBeInTheDocument();
});
it("requires refresh after a stale revision and removes cached applicants after revoked reads", async () => {
  let denied = false; vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {denied = true; return Promise.resolve(problem(409));}
    return Promise.resolve(denied ? problem(403) : Response.json({items: [vendor]}));
  })); renderPage(); const user = await selectRejection(); await user.click(screen.getByRole("button", {name: "Confirm rejection"}));
  await screen.findByText(/Refresh the directory and review/); expect(screen.queryByRole("button", {name: "Retry same decision"})).not.toBeInTheDocument(); await user.click(screen.getByRole("button", {name: "Refresh directory"})); await screen.findByRole("button", {name: "Try again"}); expect(screen.queryByText("Synthetic vendor")).not.toBeInTheDocument();
});
it("drops the old account command from the visible workspace and disables stale MFA writes", async () => {
  let finish: ((value: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => init?.method === "POST" ? new Promise<Response>((resolve) => {finish = resolve;}) : Promise.resolve(Response.json({items: [vendor]}))));
  const page = renderPage(); const user = await selectRejection(); await user.click(screen.getByRole("button", {name: "Confirm rejection"})); await screen.findByText("Recording decision…");
  page.changeSession({...current, subject_id: "next-account", selected_country: "US", assurance: {...current.assurance, fresh_auth: false}}); await screen.findByText("Use a fresh multi-factor sign-in to review applications.");
  await act(async () => {finish?.(Response.json(receipt)); await Promise.resolve();}); expect(screen.queryByText("Decision recorded: REJECTED (3)")).not.toBeInTheDocument(); expect(screen.queryByRole("button", {name: "Review rejection: Synthetic vendor"})).not.toBeInTheDocument(); expect(screen.queryByText("Synthetic vendor")).not.toBeInTheDocument();
});
it("rejects malformed or foreign owner projections and provides Tamil review labels", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({items: [{...vendor, kind: "RIDER"}]})))); await expect(getParticipants("VENDOR", "", "", "")).rejects.toThrow("Invalid participant directory");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({...receipt, participant_id: vendor.id})))); await expect(rejectParticipant({participant: vendor, key: "test-command", reason: "Documented rejection"}, current.csrf_token)).rejects.toThrow("did not match");
  expect(t("participantsTitle", "ta-IN")).toBe("பங்கேற்பாளர் பதிவு"); expect(t("participantRetryCommand", "ta-IN")).toBe("அதே முடிவை மீண்டும் முயற்சி");
});

it("shows vendor field check-in only when the owner action and current admin capability agree", async () => {
  const scheduled: Participant = {...vendor, status: "FIELD_VISIT_SCHEDULED", actions: ["FIELD_CHECK_IN"]};
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({items: [scheduled]}))));
  const noCapability = renderPage(current);
  await screen.findByText("Synthetic vendor");
  expect(screen.queryByRole("button", {name: "Vendor field check-in: Synthetic vendor"})).not.toBeInTheDocument();
  noCapability.unmount();
  renderPage({...current, capabilities: [...current.capabilities, "admin.participants.field_checkin"]});
  expect(await screen.findByRole("button", {name: "Vendor field check-in: Synthetic vendor"})).toBeInTheDocument();
});

it("loads country evidence before offering approval and sends a reasoned owner decision", async () => {
  const listed: Participant = {...vendor, status: "BANK_REVIEW", bank_status: "VERIFIED", actions: ["REJECT"]};
  const detail = {id: listed.id, participant_id: listed.participant_id, kind: "VENDOR", revision: 2, status: "BANK_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", contact_name: "Synthetic contact", documents: [{kind: "IDENTITY", ocr_status: "VERIFIED"}], field_visit: {scheduled_at: "2026-10-27T06:00:00Z", checked_in_at: "2026-10-27T07:00:00Z"}, service_zone_count: 1, bank_status: "VERIFIED", bank_last4: "1234", review_actions: ["APPROVE"], updated_at: "2026-10-27T07:00:00Z", bank_reference: "private"};
  const requests: [string, RequestInit | undefined][] = [];
  vi.stubGlobal("fetch", vi.fn((path: string, init?: RequestInit) => {
    requests.push([path, init]);
    if (init?.method === "POST") return Promise.resolve(Response.json({...receipt, status: "APPROVED", bank_reference: "private"}));
    if (path.endsWith("/review")) return Promise.resolve(Response.json(detail));
    return Promise.resolve(Response.json({items: [listed]}));
  }));
  renderPage(); await screen.findByText("Synthetic vendor");
  expect(screen.queryByRole("button", {name: "Approve application"})).not.toBeInTheDocument();
  const user = userEvent.setup(); await user.click(screen.getByRole("button", {name: "Review application: Synthetic vendor"}));
  await screen.findByText("IDENTITY: VERIFIED"); expect(screen.getByText(/••••1234/)).toBeInTheDocument(); expect(screen.queryByText("private")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", {name: "Approve application"})); await user.type(screen.getByLabelText("Reason for change"), "Verified owner evidence and bank");
  await user.click(screen.getByRole("button", {name: "Confirm review action"}));
  await screen.findByText("Decision recorded: APPROVED (3)");
  const [path, init] = requests.find((entry) => entry[1]?.method === "POST") ?? [];
  expect(path).toBe(`/admin/api/v1/participants/VENDOR/${listed.participant_id}/review-action`);
  expect(JSON.parse(init?.body as string)).toEqual({action: "APPROVE", reason: "Verified owner evidence and bank"});
  expect(new Headers(init?.headers).get("If-Match")).toBe('"2"');
});

it("retries an unconfirmed country approval with the identical command", async () => {
  const listed: Participant = {...vendor, status: "KYC_REVIEW", actions: ["REJECT"]};
  const detail = {id: listed.id, participant_id: listed.participant_id, kind: "VENDOR", revision: 2, status: "KYC_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", documents: [{kind: "IDENTITY", ocr_status: "VERIFIED"}], bank_status: "", review_actions: ["REQUIRE_FIELD_VISIT"], updated_at: "2026-10-27T07:00:00Z"};
  const commands: RequestInit[] = [];
  vi.stubGlobal("fetch", vi.fn((path: string, init?: RequestInit) => {
    if (init?.method === "POST") {commands.push(init); return commands.length === 1 ? Promise.reject(new TypeError("Response lost")) : Promise.resolve(Response.json({...receipt, status: "FIELD_VISIT_REQUIRED"}));}
    if (path.endsWith("/review")) return Promise.resolve(Response.json(detail));
    return Promise.resolve(Response.json({items: [listed]}));
  }));
  renderPage(); await screen.findByText("Synthetic vendor"); const user = userEvent.setup();
  await user.click(screen.getByRole("button", {name: "Review application: Synthetic vendor"}));
  await user.click(await screen.findByRole("button", {name: "Require field visit"}));
  await user.type(screen.getByLabelText("Reason for change"), "Verified documents require a scheduled visit");
  await user.click(screen.getByRole("button", {name: "Confirm review action"}));
  await screen.findByText(/result is unconfirmed/);
  await user.click(screen.getByRole("button", {name: "Retry same decision"}));
  await screen.findByText("Decision recorded: FIELD_VISIT_REQUIRED (3)");
  expect(commands).toHaveLength(2); expect(commands[1]?.body).toBe(commands[0]?.body);
  expect(new Headers(commands[1]?.headers).get("Idempotency-Key")).toBe(new Headers(commands[0]?.headers).get("Idempotency-Key"));
});

it("hides review details when assurance expires and never reuses them after reauthentication is denied", async () => {
  const listed: Participant = {...vendor, status: "OCR_REVIEW"};
  const detail = {id: listed.id, participant_id: listed.participant_id, kind: "VENDOR", revision: 2, status: "OCR_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", contact_name: "Private contact", documents: [{kind: "IDENTITY", ocr_status: "REVIEW_REQUIRED"}], bank_status: "", review_actions: ["VERIFY_KYC", "REJECT"], updated_at: "2026-10-27T07:00:00Z"};
  let revoked = false;
  vi.stubGlobal("fetch", vi.fn((path: string) => Promise.resolve(path.endsWith("/review") ? revoked ? problem(403) : Response.json(detail) : Response.json({items: [listed]}))));
  const page = renderPage(); const user = userEvent.setup();
  await screen.findByText("Synthetic vendor");
  await user.click(screen.getByRole("button", {name: "Review application: Synthetic vendor"}));
  expect(await screen.findByText("IDENTITY: REVIEW_REQUIRED")).toBeInTheDocument();
  expect(screen.getByRole("note")).toHaveTextContent("Document status alone does not establish identity");
  page.changeSession({...current, assurance: {...current.assurance, fresh_auth: false}});
  await screen.findAllByText("Use a fresh multi-factor sign-in to review applications.");
  expect(screen.queryByText("IDENTITY: REVIEW_REQUIRED")).not.toBeInTheDocument();
  expect(screen.queryByText(/Private contact/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", {name: "Verify KYC documents"})).not.toBeInTheDocument();
  revoked = true;
  page.changeSession({...current, assurance: {...current.assurance, auth_time: "2026-10-27T08:00:00Z"}});
  await screen.findByText("Current application is unavailable.");
  expect(screen.queryByText("IDENTITY: REVIEW_REQUIRED")).not.toBeInTheDocument();
  expect(screen.queryByText(/Private contact/)).not.toBeInTheDocument();
});

it("opens only a server-resolved private document with a fresh CSRF-bound reviewer grant", async () => {
  const listed: Participant = {...vendor, status: "OCR_REVIEW"};
  const detail = {id: listed.id, participant_id: listed.participant_id, kind: "VENDOR", revision: 2, status: "OCR_REVIEW", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", documents: [{kind: "OWNER_IDENTITY", ocr_status: "REVIEW_REQUIRED"}], bank_status: "", review_actions: ["VERIFY_KYC"], updated_at: "2026-10-27T07:00:00Z"};
  const replace = vi.fn(); const close = vi.fn();
  const popup = {opener: window, closed: false, location: {replace}, close};
  const open = vi.fn(() => popup);
  vi.stubGlobal("open", open);
  const accesses: {path: string; init?: RequestInit}[] = [];
  vi.stubGlobal("fetch", vi.fn((path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      accesses.push({path, init});
      return Promise.resolve(Response.json({kind: "OWNER_IDENTITY", url: "https://private.example/identity?signature=synthetic-secret", expires_at: new Date(Date.now() + 60_000).toISOString()}));
    }
    return Promise.resolve(path.endsWith("/review") ? Response.json(detail) : Response.json({items: [listed]}));
  }));
  const page = renderPage(); const user = userEvent.setup();
  await screen.findByText("Synthetic vendor");
  await user.click(screen.getByRole("button", {name: "Review application: Synthetic vendor"}));
  await user.click(await screen.findByRole("button", {name: "Open private document: OWNER_IDENTITY"}));
  await waitFor(() => {expect(replace).toHaveBeenCalledWith("https://private.example/identity?signature=synthetic-secret");});
  expect(open).toHaveBeenCalledWith("about:blank", "_blank");
  expect(popup.opener).toBeNull();
  expect(accesses).toHaveLength(1);
  expect(accesses[0]?.path).toBe(`/admin/api/v1/participants/VENDOR/${listed.participant_id}/review/documents/OWNER_IDENTITY/access`);
  expect(new Headers(accesses[0]?.init?.headers).get("X-CSRF-Token")).toBe(current.csrf_token);
  expect(accesses[0]?.init?.body).toBeUndefined();
  page.changeSession({...current, assurance: {...current.assurance, fresh_auth: false}});
  expect(screen.queryByRole("button", {name: "Open private document: OWNER_IDENTITY"})).not.toBeInTheDocument();
});
