import {render, screen} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach, expect, it, vi} from "vitest";
import {ParticipantFieldCheckIn} from "./participant-field-check-in";
import {adminSession} from "../test/fixtures";
import type {Participant} from "../api/participants";

const original = Object.getOwnPropertyDescriptor(navigator, "geolocation");
afterEach(() => {vi.unstubAllGlobals(); if (original) Object.defineProperty(navigator, "geolocation", original); else Reflect.deleteProperty(navigator, "geolocation");});
const participant: Participant = {id: "30000000-0000-4000-8000-000000000001", participant_id: "20000000-0000-4000-8000-000000000001", kind: "VENDOR", revision: 2, status: "FIELD_VISIT_SCHEDULED", display_name: "Synthetic vendor", category: "SOLE_PROPRIETOR", document_count: 2, bank_status: "PENDING", actions: ["FIELD_CHECK_IN"], updated_at: "2026-10-27T07:00:00Z"};
const session = {...adminSession, capabilities: [...adminSession.capabilities, "admin.participants.field_checkin"]};
const receipt = {participant_id: participant.participant_id, kind: "VENDOR", revision: 3, status: "FIELD_VISIT_PASSED"};
function location() {
  let success: PositionCallback | undefined;
  const get = vi.fn<Geolocation["getCurrentPosition"]>((yes) => {success = yes;});
  Object.defineProperty(navigator, "geolocation", {configurable: true, value: {getCurrentPosition: get}});
  return {get, succeed: () => success?.({coords: {latitude: 13.0827, longitude: 80.2707, accuracy: 5}, timestamp: Date.now()} as GeolocationPosition)};
}
function show(p = participant, authorized = session) {
  const onRecorded = vi.fn(), onDenied = vi.fn(), onClose = vi.fn();
  const view = render(<ParticipantFieldCheckIn participant={p} session={authorized} onRecorded={onRecorded} onDenied={onDenied} onClose={onClose} />);
  return {...view, onRecorded, onDenied};
}
async function capture(current: ReturnType<typeof location>) {
  await userEvent.click(screen.getByRole("button", {name: "Use current location"})); current.succeed(); await screen.findByText("Approximate accuracy: 5 m");
}

it("records an eligible vendor visit using fresh geolocation and revisioned owner command", async () => {
  const current = location(); const fetch = vi.fn(() => Promise.resolve(Response.json({...receipt, bank_reference: "private"}))); vi.stubGlobal("fetch", fetch);
  const shown = show(); expect(current.get).not.toHaveBeenCalled(); await capture(current);
  await userEvent.click(screen.getByRole("button", {name: "Confirm location and record visit"}));
  await screen.findByText(/Vendor field visit recorded/); expect(shown.onRecorded).toHaveBeenCalledTimes(1);
  const [path, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
  expect(path).toBe(`/admin/api/v1/participants/VENDOR/${participant.participant_id}/field-check-in`);
  expect(JSON.parse(init.body as string)).toEqual({latitude: 13.0827, longitude: 80.2707});
  const headers = new Headers(init.headers); expect(headers.get("If-Match")).toBe('"2"'); expect(headers.get("Idempotency-Key")).toMatch(/^[a-f0-9-]{36}$/); expect(headers.get("X-CSRF-Token")).toBe(session.csrf_token);
  expect(screen.queryByText("private")).not.toBeInTheDocument();
});

it("retries an unconfirmed visit with the same key and clears a revoked one", async () => {
  const current = location(); const requests: RequestInit[] = []; const shown = show();
  vi.stubGlobal("fetch", vi.fn((_path: string, init: RequestInit) => {requests.push(init); return Promise.resolve(requests.length === 1 ? Response.json({error: {code: "UNAVAILABLE", message: "Unavailable", correlation_id: "test", retryable: true}}, {status: 503}) : requests.length === 2 ? Response.json(receipt) : Response.json({error: {code: "DENIED", message: "Denied", correlation_id: "test", retryable: false}}, {status: 403}));}));
  await capture(current); await userEvent.click(screen.getByRole("button", {name: "Confirm location and record visit"}));
  await screen.findByText(/result is unconfirmed/); await userEvent.click(screen.getByRole("button", {name: "Retry the same visit"}));
  await screen.findByText(/Vendor field visit recorded/); expect(requests).toHaveLength(2);
  expect(requests[1]?.body).toBe(requests[0]?.body); expect(new Headers(requests[1]?.headers).get("Idempotency-Key")).toBe(new Headers(requests[0]?.headers).get("Idempotency-Key"));
  await capture(current); await userEvent.click(screen.getByRole("button", {name: "Confirm location and record visit"}));
  await screen.findByText(/assignment changed/); expect(shown.onDenied).toHaveBeenCalledTimes(1); expect(screen.queryByText("Approximate accuracy: 5 m")).not.toBeInTheDocument();
});

it("does not capture location for stale authentication, missing capability or owner-ineligible actions", () => {
  const current = location(); const stale = show(participant, {...session, assurance: {...session.assurance, fresh_auth: false}});
  expect(screen.getByRole("button", {name: "Use current location"})).toBeDisabled(); stale.unmount();
  show(participant, {...session, capabilities: []}).unmount(); show({...participant, actions: []}).unmount();
  expect(current.get).not.toHaveBeenCalled();
});
