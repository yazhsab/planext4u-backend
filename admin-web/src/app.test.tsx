import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "./app";
import { adminSession, auditPage, problem } from "./test/fixtures";

afterEach(() => vi.unstubAllGlobals());

describe("administrator application", () => {
  it("renders the server-authorized workspace and security context", async () => {
    stubFetch(() => Response.json(adminSession));
    renderApp("/");

    expect(await screen.findByRole("heading", {name: "Administration overview"})).toBeVisible();
    expect(screen.getByRole("link", {name: "Audit trail"})).toBeVisible();
    expect(screen.getByText("Multi-factor security")).toBeVisible();
    expect(screen.getAllByText("Country Admin")).toHaveLength(2);
  });

  it("does not fetch or link audit events for a support-only role", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(Response.json({
      ...adminSession,
      roles: ["SUPPORT_ADMIN"],
      capabilities: ["admin.shell.read", "admin.support.manage"],
      navigation: [{id: "workspace", label: "Workspace", path: "/", capability: "admin.shell.read"}],
    })));
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/audit");

    expect(await screen.findByRole("heading", {name: "Audit access unavailable"})).toBeVisible();
    expect(screen.queryByRole("link", {name: "Audit trail"})).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("loads tenant and country scoped audit data", async () => {
    stubFetch((input) => input.includes("/audit/events") ? Response.json(auditPage) : Response.json(adminSession));
    renderApp("/audit");

    expect(await screen.findByText("admin.session.opened")).toBeVisible();
    expect(screen.getByText("corr-synthetic-admin-001")).toBeVisible();
    expect(screen.getByText("Succeeded")).toBeVisible();
  });

  it("renders the audit empty state without inventing rows", async () => {
    stubFetch((input) => input.includes("/audit/events") ? Response.json({entries: [], has_more: false}) : Response.json(adminSession));
    renderApp("/audit");

    expect(await screen.findByRole("heading", {name: "No matching events"})).toBeVisible();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("uses the opaque server cursor for audit pagination", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = requestURL(input);
      if (path.includes("/audit/events")) {
        return Promise.resolve(Response.json(path.includes("cursor-next") ? auditPage : {...auditPage, has_more: true, next_cursor: "cursor-next"}));
      }
      return Promise.resolve(Response.json(adminSession));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/audit");
    await screen.findByText("admin.session.opened");

    fireEvent.click(screen.getByRole("button", {name: "Next"}));
    await waitFor(() => { expect(fetchMock.mock.calls.some(([input]) => requestURL(input).includes("cursor=cursor-next"))).toBe(true); });
    expect(screen.getByText("Page 2")).toBeVisible();
		const previous = screen.getByRole("button", {name: "Previous"});
		await waitFor(() => { expect(previous).toBeEnabled(); });
		fireEvent.click(previous);
		await waitFor(() => { expect(screen.getByText("Page 1")).toBeVisible(); });
  });

  it("opens and closes the responsive navigation control", async () => {
    const user = userEvent.setup();
    stubFetch(() => Response.json(adminSession));
    renderApp("/");
    const open = await screen.findByRole("button", {name: "Open navigation"});
    await user.click(open);
		const close = screen.getAllByRole("button", {name: "Close navigation"})[0];
		if (!close) throw new Error("Close navigation control missing");
		expect(close).toHaveAttribute("aria-expanded", "true");
		await user.click(close);
    expect(screen.getByRole("button", {name: "Open navigation"})).toHaveAttribute("aria-expanded", "false");
  });

  it("shows the secure sign-in state for an expired session", async () => {
    stubFetch(() => problem(401, "ADMIN_AUTHENTICATION_REQUIRED", "Sign in with an administrator account."));
    renderApp("/");

    expect(await screen.findByRole("heading", {name: "Administrator sign-in required"})).toBeVisible();
    expect(screen.getByRole("link", {name: "Go to secure sign-in"})).toHaveAttribute("href", "/login");
  });

  it("shows the MFA recovery route when assurance is insufficient", async () => {
    stubFetch(() => problem(403, "ADMIN_MFA_REQUIRED", "Complete multi-factor authentication to continue."));
    renderApp("/");

    expect(await screen.findByRole("heading", {name: "Multi-factor verification required"})).toBeVisible();
    expect(screen.getByRole("link", {name: "Verify identity"})).toHaveAttribute("href", "/login?reauth=mfa");
  });

  it("recovers when an unavailable session service succeeds on retry", async () => {
    let attempts = 0;
    stubFetch(() => {
      attempts += 1;
			return attempts === 1 ? problem(400, "ADMIN_RESPONSE_INVALID", "The service response is unavailable.") : Response.json(adminSession);
    });
    renderApp("/");
    expect(await screen.findByRole("heading", {name: "Administration service unavailable"})).toBeVisible();
    fireEvent.click(screen.getByRole("button", {name: "Try again"}));
    expect(await screen.findByRole("heading", {name: "Administration overview"})).toBeVisible();
  });

  it("changes country with the session CSRF token and refreshes context", async () => {
    let country = "IN";
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = requestURL(input);
      if (path.endsWith("/session/country")) {
        expect(init?.method).toBe("PUT");
        expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
        country = "SG";
        return Promise.resolve(new Response(null, {status: 204}));
      }
      return Promise.resolve(Response.json({...adminSession, selected_country: country}));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/");

    const selector = await screen.findByRole("combobox", {name: "Country context"});
    fireEvent.change(selector, {target: {value: "SG"}});
    await waitFor(() => expect(selector).toHaveValue("SG"));
  });

  it("validates audit filters before requesting the API", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve(requestURL(input).includes("/audit/events") ? Response.json(auditPage) : Response.json(adminSession)));
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/audit");
    await screen.findByText("admin.session.opened");
    const initialCalls = fetchMock.mock.calls.length;

    await user.type(screen.getByLabelText("Event action"), "INVALID ACTION");
    await user.click(screen.getByRole("button", {name: "Apply filter"}));
    expect(await screen.findByText(/Use a valid event action/)).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(initialCalls);
  });
});

function renderApp(initialEntry: string) {
  const queryClient = new QueryClient({defaultOptions: {queries: {retry: false, gcTime: 0}, mutations: {retry: false}}});
  return render(<QueryClientProvider client={queryClient}><MemoryRouter initialEntries={[initialEntry]}><App /></MemoryRouter></QueryClientProvider>);
}

function stubFetch(resolver: (input: string) => Response) {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(resolver(requestURL(input)))));
}

function requestURL(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.href;
  return input.url;
}
