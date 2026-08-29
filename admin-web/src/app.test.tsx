import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "./app";
import { adminSession, auditPage, governanceView, operationPage, problem } from "./test/fixtures";

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

  it("renders country-scoped privileged changes and independent approval controls", async () => {
    stubFetch((input) => input.endsWith("/operations") ? Response.json(operationPage) : Response.json(adminSession));
    renderApp("/operations");

    expect(await screen.findByRole("heading", {name: "Privileged operations"})).toBeVisible();
    expect(await screen.findByText("customer-synthetic-001")).toBeVisible();
    expect(screen.getByText("Pending Approval")).toBeVisible();
    expect(screen.getByText("Review")).toBeVisible();
  });

  it("renders the masked country governance workspace", async () => {
    stubFetch((input) => input.endsWith("/governance") ? Response.json(governanceView) : Response.json(adminSession));
    renderApp("/governance");

    expect(await screen.findByRole("heading", {name: "Governance & intelligence"})).toBeVisible();
    expect(screen.getByText("policy-IN-2026.08")).toBeVisible();
    expect(screen.getByText("Emergency within SLA")).toBeVisible();
    expect(screen.getAllByText(/PII masked/)).not.toHaveLength(0);
  });

  it("does not request governance without its server capability", async () => {
    const restricted = {
      ...adminSession,
      capabilities: ["admin.shell.read"],
      navigation: [
        {
          id: "workspace",
          label: "Workspace",
          path: "/" as const,
          capability: "admin.shell.read",
        },
      ],
    };
    const fetchMock = vi.fn(() => Promise.resolve(Response.json(restricted)));
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/governance");

    expect(
      await screen.findByRole("heading", {
        name: "Governance access unavailable",
      }),
    ).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("renders governance failures and disabled or restricted projections", async () => {
    stubFetch((input) =>
      input.endsWith("/governance")
        ? problem(403, "ADMIN_GOVERNANCE_FORBIDDEN", "Governance is unavailable.")
        : Response.json(adminSession),
    );
    const failureView = renderApp("/governance");
    expect(
      await screen.findByRole("heading", { name: "Governance could not be loaded" }),
    ).toBeVisible();
    expect(screen.getByText(/Reference: corr-synthetic-problem/)).toBeVisible();
    failureView.unmount();

    stubFetch((input) =>
      input.endsWith("/governance")
        ? Response.json({
            ...governanceView,
            metrics: [
              {
                ...governanceView.metrics[0],
                unit: "count",
                masked: false,
              },
            ],
            feature_flags: { emergency: false },
          })
        : Response.json(adminSession),
    );
    renderApp("/governance");
    expect(await screen.findByText("Restricted", { exact: false })).toBeVisible();
    expect(screen.getByText("Disabled")).toBeVisible();
  });

  it("submits a controlled operation with CSRF and correlation evidence", async () => {
    const user = userEvent.setup();
    const executed = {...operationPage.changes[0], command: {...operationPage.changes[0]?.command, domain: "CATALOG" as const, action: "UPSERT", target_id: "item-synthetic-002"}, risk: "STANDARD" as const, status: "EXECUTED" as const};
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = requestURL(input);
      if (path.endsWith("/operations") && init?.method === "POST") {
        expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(adminSession.csrf_token);
        expect(new Headers(init.headers).get("X-Correlation-ID")).toMatch(/^admin-web-/);
        expect(init.body).toContain("item-synthetic-002");
        return Promise.resolve(Response.json(executed, {status: 201}));
      }
      if (path.endsWith("/operations")) return Promise.resolve(Response.json(operationPage));
      return Promise.resolve(Response.json(adminSession));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/operations");
    await screen.findByText("customer-synthetic-001");

    await user.type(screen.getByLabelText("Target identifier"), "item-synthetic-002");
    await user.type(screen.getByLabelText("Verified reason"), "Publish verified catalogue information");
    await user.click(screen.getByRole("button", {name: "Submit controlled change"}));

    expect(await screen.findByText("Change executed.")).toBeVisible();
  });

  it("does not request operations without the server navigation capability", async () => {
    const restrictedSession = {...adminSession, capabilities: ["admin.shell.read"], navigation: [{id: "workspace", label: "Workspace", path: "/" as const, capability: "admin.shell.read"}]};
    const fetchMock = vi.fn(() => Promise.resolve(Response.json(restrictedSession)));
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/operations");

    expect(await screen.findByRole("heading", {name: "Operations access unavailable"})).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("renders an explicit state when no operational domain is assigned", async () => {
    const restrictedSession = {...adminSession, capabilities: ["admin.shell.read", "admin.operations.read"]};
    stubFetch(() => Response.json(restrictedSession));
    renderApp("/operations");

    expect(await screen.findByRole("heading", {name: "No operational domain assigned"})).toBeVisible();
  });

  it("renders empty and permission failure operation states", async () => {
    stubFetch((input) => input.endsWith("/operations") ? Response.json({changes: []}) : Response.json(adminSession));
    const view = renderApp("/operations");
    expect(await screen.findByRole("heading", {name: "No changes in this country"})).toBeVisible();
    view.unmount();

    stubFetch((input) => input.endsWith("/operations") ? problem(403, "ADMIN_OPERATION_FORBIDDEN", "Operations are not available for this role.") : Response.json(adminSession));
    renderApp("/operations");
    expect(await screen.findByRole("heading", {name: "Operations could not be loaded"})).toBeVisible();
    expect(screen.getByText(/Reference: corr-synthetic-problem/)).toBeVisible();
  });

  it("validates operation identifiers, reasons, and sensitive payloads before submit", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve(Response.json(requestURL(input).endsWith("/operations") ? operationPage : adminSession)));
    vi.stubGlobal("fetch", fetchMock);
    renderApp("/operations");
    await screen.findByText("customer-synthetic-001");
    const initialCalls = fetchMock.mock.calls.length;

    await user.type(screen.getByLabelText("Target identifier"), "invalid target");
    await user.type(screen.getByLabelText("Verified reason"), "short");
    fireEvent.change(screen.getByLabelText("Operation payload (JSON object)"), {target: {value: `{"provider_secret":"unsafe"}`}});
    await user.click(screen.getByRole("button", {name: "Submit controlled change"}));

    expect(await screen.findByText(/Use letters, numbers/)).toBeVisible();
    expect(screen.getByText(/at least 8 characters/)).toBeVisible();
    expect(screen.getByText(/without password, token, or secret fields/)).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(initialCalls);
  });

  it("requires another administrator and supports approve or reject decisions", async () => {
    const user = userEvent.setup();
    const selfPage = {changes: [{...operationPage.changes[0], requested_by: adminSession.subject_id}]};
    stubFetch((input) => input.endsWith("/operations") ? Response.json(selfPage) : Response.json(adminSession));
    const view = renderApp("/operations");
    expect(await screen.findByText("Awaiting another administrator")).toBeVisible();
    view.unmount();

    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = requestURL(input);
      if (path.endsWith("/approve")) return Promise.resolve(Response.json({...operationPage.changes[0], status: "EXECUTED", approved_by: adminSession.subject_id, revision: 2}));
      if (path.endsWith("/reject")) return Promise.resolve(Response.json({...operationPage.changes[0], status: "REJECTED", approved_by: adminSession.subject_id, revision: 2}));
      if (path.endsWith("/operations")) return Promise.resolve(Response.json(operationPage));
      return Promise.resolve(Response.json(adminSession));
    });
    vi.stubGlobal("fetch", fetchMock);
    const approvalView = renderApp("/operations");
    await screen.findByText("customer-synthetic-001");
    await user.click(screen.getByText("Review"));
    await user.click(screen.getByRole("button", {name: "Approve"}));
    await waitFor(() => { expect(fetchMock.mock.calls.some(([input]) => requestURL(input).endsWith("/approve"))).toBe(true); });
    approvalView.unmount();

    vi.stubGlobal("fetch", fetchMock);
    renderApp("/operations");
    await screen.findByText("customer-synthetic-001");
    await user.click(screen.getByText("Review"));
    await user.type(screen.getByLabelText("Rejection reason"), "Independent review found invalid evidence");
    await user.click(screen.getByRole("button", {name: "Reject"}));
    await waitFor(() => { expect(fetchMock.mock.calls.some(([input]) => requestURL(input).endsWith("/reject"))).toBe(true); });
  });

  it("blocks privileged submission when fresh authentication has expired", async () => {
    stubFetch((input) => input.endsWith("/operations") ? Response.json(operationPage) : Response.json({...adminSession, assurance: {...adminSession.assurance, fresh_auth: false}}));
    renderApp("/operations");
    await screen.findByText("customer-synthetic-001");
    expect(screen.getByRole("button", {name: "Submit controlled change"})).toBeDisabled();
    expect(screen.getByRole("link", {name: "Re-authenticate before submitting"})).toHaveAttribute("href", "/login?reauth=mfa");
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
