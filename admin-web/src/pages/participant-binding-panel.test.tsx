import {QueryClient, QueryClientProvider} from "@tanstack/react-query";
import {render, screen, waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach, expect, it, vi} from "vitest";
import {ParticipantBindingPanel} from "./participant-binding-panel";
import {adminSession} from "../test/fixtures";
import type {Participant} from "../api/participants";

const node = "50000000-0000-4000-8000-000000000001";
const participant: Participant = {id: "30000000-0000-4000-8000-000000000001", participant_id: "20000000-0000-4000-8000-000000000001", kind: "VENDOR", revision: 4, status: "REJECTED", display_name: "Correction vendor", category: "SOLE_PROPRIETOR", document_count: 2, bank_status: "", actions: [], updated_at: "2026-10-27T10:00:00Z"};
afterEach(() => {vi.unstubAllGlobals(); vi.restoreAllMocks();});
function setup({fresh = true, fail = false}: {fresh?: boolean; fail?: boolean} = {}) {
  let revision = 0; const commands: RequestInit[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "PUT") {
      commands.push(init);
      if (fail) return Promise.resolve(Response.json({error: {code: "CONFLICT", message: "Refresh current revision", correlation_id: "binding-test", retryable: false}}, {status: 409}));
      revision++; return Promise.resolve(Response.json({kind: "VENDOR", participant_id: participant.participant_id, node_id: node, active: true, revision}));
    }
    return Promise.resolve(Response.json(url.includes("participant-territories") ? {items: [{id: node, name: "Chennai North"}]} : {kind: "VENDOR", participant_id: participant.participant_id, ...(revision ? {node_id: node} : {}), active: revision > 0, revision}));
  }));
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: false}}});
  render(<QueryClientProvider client={client}><ParticipantBindingPanel participant={participant} session={{...adminSession, assurance: {...adminSession.assurance, fresh_auth: fresh}}} onClose={() => undefined} /></QueryClientProvider>);
  return commands;
}
const fill = async () => {const user = userEvent.setup(); await screen.findByRole("option", {name: "Chennai North"}); await user.selectOptions(screen.getByRole("combobox", {name: "Participant territory"}), node); await user.type(screen.getByLabelText("Reason for change"), "Assign participant to verified territory"); return user;};
it("saves a current revision and reason using session CSRF", async () => {
  const commands = setup(); const user = await fill(); await user.click(screen.getByRole("button", {name: "Save participant territory"}));
  expect(await screen.findByText("Participant territory saved with its change history.")).toBeInTheDocument();
  expect(commands).toHaveLength(1); expect(new Headers(commands[0]?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
  expect(JSON.parse(commands[0]?.body as string)).toEqual({node_id: node, active: true, revision: 0, reason: "Assign participant to verified territory"});
});
it("requires a fresh session before changing a binding", async () => {
  const commands = setup({fresh: false}); await screen.findByRole("option", {name: "Chennai North"});
  expect(screen.getByRole("button", {name: "Save participant territory"})).toBeDisabled(); expect(commands).toHaveLength(0);
});
it("requires authoritative refresh after an unconfirmed or stale write", async () => {
  const commands = setup({fail: true}); const user = await fill(); await user.click(screen.getByRole("button", {name: "Save participant territory"}));
  expect(await screen.findByRole("alert")).toHaveTextContent("Refresh to read the current revision");
  expect(screen.getByRole("button", {name: "Save participant territory"})).toBeDisabled();
  await user.click(screen.getByRole("button", {name: "Refresh directory"}));
  await waitFor(() => {expect(screen.getByRole("button", {name: "Save participant territory"})).toBeEnabled();}); expect(commands).toHaveLength(1);
});
