import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, Database, FileBarChart, LockKeyhole, RefreshCw, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { Link, useOutletContext, useParams } from "react-router";

import { APIError, type AdminSession, getReportDetail } from "../api/client";
import { Badge } from "../components/badge";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";

export function ReportDetailPage() {
  const session = useOutletContext<AdminSession>();
  const {reportId = ""} = useParams();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [applied, setApplied] = useState({from: "", to: ""});
  const canRead = session.capabilities.includes("admin.governance.read");
  const query = useQuery({
    queryKey: ["admin", "report-detail", session.selected_country, reportId, applied],
    queryFn: ({signal}) => getReportDetail(reportId, {
      from: applied.from ? new Date(`${applied.from}T00:00:00.000Z`).toISOString() : undefined,
      to: applied.to ? new Date(`${applied.to}T23:59:59.999Z`).toISOString() : undefined,
      limit: 50,
    }, signal),
    enabled: canRead && Boolean(reportId),
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });

  if (!canRead) return <StatePanel icon={LockKeyhole} title="Report access unavailable" message="Your server-authorized role cannot view governed country reports." />;
  if (query.isPending) return <div className="table-skeleton" role="status" aria-label="Loading report detail">{Array.from({length: 5}, (_, index) => <span key={index} />)}</div>;
  if (query.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="Report detail could not be loaded" message={errorMessage(query.error)} actionLabel="Try again" onAction={() => { void query.refetch(); }} />;

  const detail = query.data;
  return <div className="page-stack">
    <Button asChild variant="quiet"><Link to="/reports"><ArrowLeft aria-hidden="true" size={16} />Back to reports</Link></Button>
    <header className="page-header"><div><p className="eyebrow">{detail.report.domain} · {detail.report.metric}</p><h1>{detail.report.title}</h1></div><Badge tone="success"><ShieldCheck aria-hidden="true" size={14} />{detail.country} · PII masked</Badge></header>
    <p className="page-intro">This server-generated detail uses the selected country and optional date range. The response carries its source projection, aggregation rule, and freshness timestamp.</p>

    <section className="report-context" aria-label="Report lineage">
      <article><FileBarChart aria-hidden="true" /><span>Current value</span><strong>{formatValue(detail.report.value, detail.report.unit)}</strong><small>{detail.report.unit.replaceAll("_", " ")}</small></article>
      <article><Database aria-hidden="true" /><span>Source projection</span><strong>{detail.lineage.source_projection}</strong><small>{detail.lineage.aggregation.replaceAll("_", " ")}</small></article>
      <article><RefreshCw aria-hidden="true" /><span>Data freshness</span><strong>{formatDateTime(detail.lineage.freshness)}</strong><small>Generated {formatDateTime(detail.lineage.generated_at)}</small></article>
    </section>

    <section className="table-section" aria-labelledby="report-filter-heading">
      <div className="section-heading"><div><h2 id="report-filter-heading">Server-side filters</h2><p>UTC inclusive date range</p></div></div>
      <form className="report-filter-form" onSubmit={(event) => { event.preventDefault(); setApplied({from, to}); }}>
        <label><span>From</span><input type="date" value={from} max={to || undefined} onChange={(event) => { setFrom(event.target.value); }} /></label>
        <label><span>To</span><input type="date" value={to} min={from || undefined} onChange={(event) => { setTo(event.target.value); }} /></label>
        <Button type="submit">Apply filters</Button>
        <Button type="button" variant="quiet" onClick={() => { setFrom(""); setTo(""); setApplied({from: "", to: ""}); }}>Clear</Button>
      </form>
    </section>

    <section className="table-section" aria-labelledby="report-rows-heading">
      <div className="section-heading"><div><h2 id="report-rows-heading">Aggregate rows</h2><p>{detail.items.length.toLocaleString()} visible row(s)</p></div></div>
      {detail.items.length === 0 ? <StatePanel icon={FileBarChart} title="No rows in this date range" message="Change or clear the server-side date filters." /> : <div className="table-scroll" tabIndex={0} aria-label="Scrollable report detail table"><table className="reports-table"><thead><tr><th scope="col">Label</th><th scope="col">Dimensions</th><th scope="col">Value</th><th scope="col">Unit</th></tr></thead><tbody>{detail.items.map((row) => <tr key={`${row.label}-${JSON.stringify(row.dimensions)}`}><td><span className="primary-cell">{row.label}</span></td><td>{Object.entries(row.dimensions).map(([key, value]) => `${key}: ${value}`).join(" · ")}</td><td>{row.value.toLocaleString()}</td><td>{row.unit.replaceAll("_", " ")}</td></tr>)}</tbody></table></div>}
    </section>
  </div>;
}

function formatValue(value: number, unit: string): string {
  return unit === "percent" ? `${value.toLocaleString()}%` : value.toLocaleString();
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(value));
}

function errorMessage(error: Error | null): string {
  return error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "The report could not be loaded. Check your connection and try again.";
}
