import { useMutation, useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Download, FileBarChart, LockKeyhole, RefreshCw, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { Link, useOutletContext } from "react-router";

import {
  APIError,
  type AdminSession,
  type ReportSummary,
  createReportExport,
  getGovernance,
  getReportExport,
  listReports,
} from "../api/client";
import { Badge } from "../components/badge";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";

export function ReportsPage() {
  const session = useOutletContext<AdminSession>();
  const canRead = session.capabilities.includes("admin.governance.read");
  const canExport = session.capabilities.includes("admin.reporting.export");
  const governanceQuery = useQuery({
    queryKey: ["admin", "governance", session.selected_country],
    queryFn: ({signal}) => getGovernance(signal),
    enabled: canRead,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });
  const reportQuery = useQuery({
    queryKey: ["admin", "reports", session.selected_country],
    queryFn: ({signal}) => listReports({limit: 50}, signal),
    enabled: canRead,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });

  if (!canRead) return <StatePanel icon={LockKeyhole} title="Reports access unavailable" message="Your server-authorized role cannot view governed country reports." />;
  if (governanceQuery.isPending || reportQuery.isPending) return <div className="table-skeleton" role="status" aria-label="Loading governed reports">{Array.from({length: 6}, (_, index) => <span key={index} />)}</div>;
  if (governanceQuery.isError || reportQuery.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="Reports could not be loaded" message={reportErrorMessage(governanceQuery.error ?? reportQuery.error)} actionLabel="Try again" onAction={() => { void governanceQuery.refetch(); void reportQuery.refetch(); }} />;

  const view = governanceQuery.data;
  const reports = reportQuery.data.items;
  return (
    <div className="page-stack">
      <header className="page-header">
        <div><p className="eyebrow">Governed reporting</p><h1>Country reports</h1></div>
        <Badge tone="success"><ShieldCheck aria-hidden="true" size={14} /> {view.country} · aggregate &amp; masked</Badge>
      </header>
      <p className="page-intro">Review server-generated operational metrics for the selected country. Export requests require an explicitly authorized, fresh MFA session and are submitted through the audited administration operation boundary.</p>

      <section className="report-context" aria-label="Report projection context">
        <article><FileBarChart aria-hidden="true" /><span>Available reports</span><strong>{reports.length.toLocaleString()}</strong><small>Country-scoped projection only</small></article>
        <article><ShieldCheck aria-hidden="true" /><span>Privacy mode</span><strong>Aggregate</strong><small>No direct identity or contact fields</small></article>
        <article><RefreshCw aria-hidden="true" /><span>Projection generated</span><strong>{formatDate(view.generated_at)}</strong><small>Each metric carries its own freshness time</small></article>
      </section>

      <section className="table-section" aria-labelledby="country-reports-heading">
        <div className="section-heading">
          <div><h2 id="country-reports-heading">Report register</h2><p>{view.country} · {view.privacy_mode.replaceAll("_", " ")}</p></div>
          <Button variant="quiet" disabled={governanceQuery.isFetching || reportQuery.isFetching} onClick={() => { void governanceQuery.refetch(); void reportQuery.refetch(); }}><RefreshCw aria-hidden="true" size={16} />Refresh</Button>
        </div>
        {reports.length === 0 ? <StatePanel icon={FileBarChart} title="No published report metrics" message="No governed report projections are published for the selected country." /> : null}
        {reports.length > 0 ? (
          <div className="table-scroll" tabIndex={0} aria-label="Scrollable governed reports table">
            <table className="reports-table">
              <thead><tr><th scope="col">Report</th><th scope="col">Value</th><th scope="col">Freshness</th><th scope="col">Privacy</th><th scope="col">Export</th></tr></thead>
              <tbody>{reports.map((metric) => (
                <tr key={metric.id}>
                  <td><Link className="primary-cell" to={`/reports/${encodeURIComponent(metric.id)}`}>{metric.title}</Link><br /><code>{metric.domain} · {metric.metric}</code></td>
                  <td>{formatMetric(metric)}</td>
                  <td>{formatDateTime(metric.freshness)}</td>
                  <td><Badge tone={metric.masked ? "success" : "warning"}>{metric.masked ? "PII masked" : "Restricted"}</Badge></td>
                  <td><ReportExportControl metric={metric} session={session} canExport={canExport} /></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        ) : null}
      </section>
    </div>
  );
}

function ReportExportControl({metric, session, canExport}: {metric: ReportSummary; session: AdminSession; canExport: boolean}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const mutation = useMutation({
    mutationFn: () => createReportExport(metric.id, {format: "CSV", reason: reason.trim()}, session.csrf_token),
    onSuccess: () => { setOpen(false); },
  });
  const exportID = mutation.data?.id ?? "";
  const status = useQuery({
    queryKey: ["admin", "report-export", exportID],
    queryFn: ({signal}) => getReportExport(exportID, signal),
    enabled: mutation.data?.status === "PROCESSING",
    refetchInterval: (query) => query.state.data?.status === "PROCESSING" ? 1_000 : false,
  });
  const result = status.data ?? mutation.data;

  if (!canExport) return <span className="quiet-status">Export not authorized</span>;
  if (result?.status === "READY" && result.download_url) return <span className="report-export-success" role="status"><CheckCircle2 aria-hidden="true" size={15} /><a href={result.download_url} download={result.file_name}>Download CSV</a></span>;
  if (result?.status === "PROCESSING") return <span className="quiet-status" role="status">Preparing export…</span>;

  return (
    <div className="report-export-control">
      <Button
        type="button"
        variant="secondary"
        aria-label={`Request CSV export for ${metric.title}`}
        disabled={!session.assurance.fresh_auth}
        onClick={() => { setOpen((value) => !value); }}
      >
        <Download aria-hidden="true" size={15} />Request CSV
      </Button>
      {!session.assurance.fresh_auth ? <a href="/login?reauth=mfa">Re-authenticate to export</a> : null}
      {open ? (
        <form className="report-export-form" onSubmit={(event) => { event.preventDefault(); if (reason.trim().length >= 8) mutation.mutate(); }}>
          <label><span>Verified export reason</span><input value={reason} onChange={(event) => { setReason(event.target.value); }} minLength={8} maxLength={500} autoComplete="off" /></label>
          <div><Button type="submit" disabled={reason.trim().length < 8 || mutation.isPending}>{mutation.isPending ? "Submitting…" : "Submit request"}</Button><Button type="button" variant="quiet" onClick={() => { setOpen(false); }}>Cancel</Button></div>
          <small>At least 8 characters. The operation is country-scoped and audit recorded.</small>
          {mutation.isError ? <p role="alert">{reportErrorMessage(mutation.error)}</p> : null}
        </form>
      ) : null}
    </div>
  );
}

function formatMetric(metric: ReportSummary): string {
  if (metric.unit === "percent") return `${metric.value.toLocaleString()}%`;
  if (metric.unit === "count") return metric.value.toLocaleString();
  return `${metric.value.toLocaleString()} ${metric.unit.replaceAll("_", " ")}`;
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {dateStyle: "medium"}).format(new Date(value));
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(value));
}

function reportErrorMessage(error: Error | null): string {
  return error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "The report operation could not be completed. Check your connection and try again.";
}
