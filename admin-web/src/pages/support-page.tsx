import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Headphones, LockKeyhole } from "lucide-react";
import { useOutletContext } from "react-router";

import { APIError, type AdminSession, listSupportTickets, type SupportTicketFilters } from "../api/client";
import { Badge } from "../components/badge";
import { StatePanel } from "../components/state-panel";

type OwnerRoleFilter = NonNullable<SupportTicketFilters["ownerRole"]> | "ALL";
type StatusFilter = NonNullable<SupportTicketFilters["status"]> | "ALL";

export function SupportPage() {
  const session = useOutletContext<AdminSession>();
  const allowed = session.capabilities.includes("admin.support.manage");
  const [ownerRole, setOwnerRole] = useState<OwnerRoleFilter>("ALL");
  const [status, setStatus] = useState<StatusFilter>("ALL");
  const [cursor, setCursor] = useState<string | undefined>();
  const query = useQuery({
    queryKey: ["admin", "support", session.selected_country, ownerRole, status, cursor],
    queryFn: ({signal}) => listSupportTickets({
      ownerRole: ownerRole === "ALL" ? undefined : ownerRole,
      status: status === "ALL" ? undefined : status,
      cursor,
      limit: 50,
    }, signal),
    enabled: allowed,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });

  if (!allowed) return <StatePanel icon={LockKeyhole} title="Support access unavailable" message="Your role cannot inspect the country support queue." />;
  if (query.isPending) return <div className="table-skeleton" role="status" aria-label="Loading support queue">{Array.from({length: 6}, (_, index) => <span key={index} />)}</div>;
  if (query.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="Support queue could not be loaded" message={query.error instanceof APIError ? `${query.error.message} Reference: ${query.error.correlationID}` : "Check your connection and try again."} actionLabel="Try again" onAction={() => { void query.refetch(); }} />;

  return <div className="page-stack">
    <header className="page-header"><div><p className="eyebrow">Country operations</p><h1>Support queue</h1></div><Badge>{session.selected_country} · tenant scoped</Badge></header>
    <p className="page-intro">Inspect role-owned support requests for the selected country. Identity references are opaque and contact details are not exposed.</p>
    <section className="filter-bar" aria-label="Support filters">
      <Headphones aria-hidden="true" />
      <div className="field-group"><label htmlFor="support-owner-role">Owner role</label><select id="support-owner-role" value={ownerRole} onChange={(event) => { setOwnerRole(event.target.value as OwnerRoleFilter); setCursor(undefined); }}><option value="ALL">All roles</option><option value="CUSTOMER">Customer</option><option value="VENDOR">Vendor</option><option value="RIDER">Rider</option></select></div>
      <div className="field-group"><label htmlFor="support-status">Status</label><select id="support-status" value={status} onChange={(event) => { setStatus(event.target.value as StatusFilter); setCursor(undefined); }}><option value="ALL">All statuses</option><option value="OPEN">Open</option><option value="WAITING_FOR_SUPPORT">Waiting for support</option><option value="WAITING_FOR_REQUESTER">Waiting for requester</option><option value="RESOLVED">Resolved</option><option value="CLOSED">Closed</option></select></div>
    </section>
    <section className="table-section" aria-labelledby="support-table-heading">
      <div className="section-heading"><div><h2 id="support-table-heading">Requests</h2><p>{query.data.items.length} tickets in this page</p></div></div>
      {query.data.items.length === 0 ? <StatePanel icon={Headphones} title="No matching support tickets" message="Change the role or status filter to inspect another queue." /> : <div className="table-scroll" tabIndex={0}><table><thead><tr><th>Ticket</th><th>Role</th><th>Owner reference</th><th>Category</th><th>Priority</th><th>Status</th><th>Messages</th><th>Last activity</th></tr></thead><tbody>{query.data.items.map((ticket) => <tr key={ticket.id}><td><span className="primary-cell">{ticket.subject}</span><br /><code>{ticket.id}</code></td><td>{readable(ticket.owner_role)}</td><td><code>{ticket.owner_reference}</code></td><td>{readable(ticket.category)}</td><td><Badge tone={ticket.priority === "URGENT" ? "danger" : ticket.priority === "HIGH" ? "warning" : "neutral"}>{readable(ticket.priority)}</Badge></td><td><Badge tone={ticket.status === "RESOLVED" || ticket.status === "CLOSED" ? "success" : ticket.status === "WAITING_FOR_SUPPORT" ? "warning" : "neutral"}>{readable(ticket.status)}</Badge></td><td>{ticket.message_count}</td><td>{formatDateTime(ticket.last_message_at)}</td></tr>)}</tbody></table></div>}
      {query.data.next_cursor ? <div className="pagination"><span>More tickets are available</span><button type="button" onClick={() => { setCursor(query.data.next_cursor); }}>Next page</button></div> : null}
    </section>
  </div>;
}

function readable(value: string): string {
  return value.toLowerCase().split("_").map((word) => (word[0]?.toUpperCase() ?? "") + word.slice(1)).join(" ");
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(value));
}
