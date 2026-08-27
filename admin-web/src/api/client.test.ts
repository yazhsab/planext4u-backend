import { afterEach, describe, expect, it, vi } from "vitest";

import { APIError, getSession, listAuditEvents, setCountry } from "./client";
import { adminSession, auditPage, problem } from "../test/fixtures";

afterEach(() => vi.unstubAllGlobals());

describe("admin API client", () => {
  it("validates successful responses against the runtime contract", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json(adminSession))));
    await expect(getSession()).resolves.toEqual(adminSession);
  });

  it("rejects malformed successful responses", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(Response.json({...adminSession, selected_country: "India"}))));
    await expect(getSession()).rejects.toMatchObject({code: "ADMIN_CONTRACT_INVALID"});
  });

  it("preserves safe problem codes and correlation identifiers", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(problem(403, "ADMIN_ROLE_FORBIDDEN", "Access denied."))));
    await expect(getSession()).rejects.toEqual(expect.objectContaining<Partial<APIError>>({status: 403, code: "ADMIN_ROLE_FORBIDDEN", correlationID: "corr-synthetic-problem"}));
  });

  it("encodes bounded audit filters", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
			expect(requestURL(input)).toContain("/admin/api/v1/audit/events");
			return Promise.resolve(Response.json(auditPage));
		});
    vi.stubGlobal("fetch", fetchMock);
    await listAuditEvents({action: "admin.session.opened", cursor: "cursor-1", limit: 25});
		const firstCall = fetchMock.mock.calls[0];
		expect(firstCall).toBeDefined();
		if (!firstCall) throw new Error("Expected an audit request");
		expect(requestURL(firstCall[0])).toContain("action=admin.session.opened&cursor=cursor-1&limit=25");
  });

  it("uses credentials and CSRF protection for country changes", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, {status: 204})));
    vi.stubGlobal("fetch", fetchMock);
    await setCountry("SG", adminSession.csrf_token);
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/v1/session/country", expect.objectContaining({method: "PUT", credentials: "same-origin"}));
  });

  it("reports a safe fallback when the error envelope is unreadable", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("gateway failure", {status: 502}))));
    await expect(getSession()).rejects.toMatchObject({status: 502, code: "ADMIN_RESPONSE_INVALID"});
  });

  it("propagates a country denial without exposing response details", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(problem(403, "ADMIN_COUNTRY_FORBIDDEN", "You do not have access to that country."))));
    await expect(setCountry("US", adminSession.csrf_token)).rejects.toMatchObject({status: 403, code: "ADMIN_COUNTRY_FORBIDDEN"});
  });
});

function requestURL(input: RequestInfo | URL): string {
	if (typeof input === "string") return input;
	if (input instanceof URL) return input.href;
	return input.url;
}
