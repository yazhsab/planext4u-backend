import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, BarChart3, Flag, LockKeyhole, ShieldCheck } from "lucide-react";
import { useOutletContext } from "react-router";

import { APIError, type AdminSession, getGovernance } from "../api/client";
import { Badge } from "../components/badge";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";

export function GovernancePage() {
  const session = useOutletContext<AdminSession>();
  const allowed = session.capabilities.includes("admin.governance.read");
  const query = useQuery({
    queryKey: ["admin", "governance", session.selected_country],
    queryFn: ({signal}) => getGovernance(signal),
    enabled: allowed,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });
  if (!allowed) return <StatePanel icon={LockKeyhole} title="Governance access unavailable" message="Your role cannot view governed country configuration and intelligence." />;
  if (query.isPending) return <div className="table-skeleton" role="status" aria-label="Loading governance workspace">{Array.from({length: 5}, (_, index) => <span key={index} />)}</div>;
  if (query.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="Governance could not be loaded" message={query.error instanceof APIError ? `${query.error.message} Reference: ${query.error.correlationID}` : "Check your connection and try again."} actionLabel="Try again" onAction={() => { void query.refetch(); }} />;

  const view = query.data;
  return <div className="page-stack">
    <header className="page-header"><div><p className="eyebrow">Country control plane</p><h1>Governance & intelligence</h1></div><Badge tone="success"><ShieldCheck aria-hidden="true" size={14} /> Aggregate & masked</Badge></header>
    <p className="page-intro">Review country policy, feature rollout and operational intelligence. Changes are submitted through controlled operations with fresh MFA, audit evidence and independent approval.</p>
    <section className="governance-summary" aria-label="Governance metrics">
      {view.metrics.map((metric) => <article key={metric.id} className="metric-card"><BarChart3 aria-hidden="true" /><span>{metric.title}</span><strong>{metric.value.toLocaleString()}{metric.unit === "percent" ? "%" : ""}</strong><small>{metric.masked ? "PII masked" : "Restricted"} · refreshed {new Intl.DateTimeFormat(undefined, {timeStyle: "short"}).format(new Date(metric.freshness))}</small></article>)}
    </section>
    <section className="governance-controls">
      <div><p className="eyebrow">Policy</p><h2>{view.policy_version}</h2><p>Active configuration for {view.country}. Publishing or rollback is handled as a governed operation.</p></div>
      <div><p className="eyebrow">Feature rollout</p><h2>Country flags</h2><ul className="flag-list">{Object.entries(view.feature_flags).map(([name, enabled]) => <li key={name}><Flag aria-hidden="true" size={15} /><span>{name}</span><Badge tone={enabled ? "success" : "neutral"}>{enabled ? "Enabled" : "Disabled"}</Badge></li>)}</ul></div>
    </section>
    <div className="governance-action"><Button onClick={() => { globalThis.location.assign("/operations"); }}>Open controlled operations</Button><small>Exports require MFA and are recorded in the audit trail.</small></div>
  </div>;
}
