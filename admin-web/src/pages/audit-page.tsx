import { zodResolver } from "@hookform/resolvers/zod";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { createColumnHelper, flexRender, getCoreRowModel, useReactTable } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, Filter, Search, ShieldAlert } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useOutletContext } from "react-router";
import { z } from "zod";

import { APIError, listAuditEvents, type AdminSession, type AuditEntry } from "../api/client";
import { Badge } from "../components/badge";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";

const filterSchema = z.object({action: z.string().trim().max(128).refine((value) => value === "" || /^[a-z][a-z0-9_.]{2,127}$/.test(value), "Use a valid event action, such as admin.session.opened.")});
type FilterValues = z.infer<typeof filterSchema>;
const columns = createColumnHelper<AuditEntry>();
const tableColumns = [
  columns.accessor("occurred_at", {header: "Time", cell: ({getValue}) => <time dateTime={getValue()}>{formatDate(getValue())}</time>}),
  columns.accessor("action", {header: "Action", cell: ({getValue}) => <span className="primary-cell">{getValue()}</span>}),
  columns.accessor("actor.subject_id", {header: "Actor"}),
  columns.accessor("target", {header: "Target", cell: ({getValue}) => `${getValue().type} · ${getValue().id}`}),
  columns.accessor("outcome", {header: "Outcome", cell: ({getValue}) => <Badge tone={outcomeTone(getValue())}>{readable(getValue())}</Badge>}),
  columns.accessor("correlation_id", {header: "Correlation ID", cell: ({getValue}) => <code>{getValue()}</code>}),
];

export function AuditPage() {
  const session = useOutletContext<AdminSession>();
	const canReadAudit = session.capabilities.includes("admin.audit.read");
  const [action, setAction] = useState("");
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const cursor = cursorStack.at(-1) ?? "";
  const form = useForm<FilterValues>({resolver: zodResolver(filterSchema), defaultValues: {action: ""}});
  const query = useQuery({
    queryKey: ["admin", "audit", session.selected_country, action, cursor],
    queryFn: ({signal}) => listAuditEvents({action: action || undefined, cursor: cursor || undefined, limit: 50}, signal),
		enabled: canReadAudit,
    placeholderData: keepPreviousData,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });
  const table = useReactTable({data: query.data?.entries ?? [], columns: tableColumns, getCoreRowModel: getCoreRowModel()});

  if (!canReadAudit) {
    return <StatePanel icon={ShieldAlert} tone="danger" title="Audit access unavailable" message="Your administrator role does not grant access to audit events." />;
  }

  const applyFilter = ({action: nextAction}: FilterValues) => {
    setAction(nextAction);
    setCursorStack([""]);
  };

  return (
    <div className="page-stack">
      <header className="page-header">
        <div><p className="eyebrow">Compliance evidence</p><h1>Audit trail</h1></div>
        <Badge>{session.selected_country} · tenant scoped</Badge>
      </header>
      <p className="page-intro">Review immutable administrative and system activity. Results are restricted to the selected country and your authenticated tenant.</p>

      <form className="filter-bar" onSubmit={form.handleSubmit(applyFilter)} noValidate>
        <Filter aria-hidden="true" size={18} />
        <div className="field-group">
          <label htmlFor="audit-action">Event action</label>
          <div className="input-wrap"><Search aria-hidden="true" size={17} /><input id="audit-action" placeholder="e.g. admin.session.opened" autoComplete="off" {...form.register("action")} /></div>
          {form.formState.errors.action ? <p className="field-error" id="audit-action-error">{form.formState.errors.action.message}</p> : null}
        </div>
        <Button type="submit">Apply filter</Button>
      </form>

      <section className="table-section" aria-labelledby="audit-results-heading">
        <div className="section-heading">
					<div><h2 id="audit-results-heading">Events</h2><p>{query.data ? `${String(query.data.entries.length)} events on this page` : "Loading authorized events"}</p></div>
          {query.isFetching && query.data ? <span className="quiet-status" role="status">Refreshing…</span> : null}
        </div>

        {query.isPending ? <AuditTableSkeleton /> : null}
        {query.isError ? <StatePanel icon={ShieldAlert} tone="danger" title="Audit events could not be loaded" message={errorMessage(query.error)} actionLabel="Try again" onAction={() => void query.refetch()} /> : null}
        {query.isSuccess && query.data.entries.length === 0 ? <StatePanel icon={Search} title="No matching events" message="No audit events match this country and filter. Try a broader event action." /> : null}
        {query.isSuccess && query.data.entries.length > 0 ? (
						<div className="table-scroll" tabIndex={0} role="region" aria-label="Scrollable audit events table">
            <table>
              <thead>{table.getHeaderGroups().map((group) => <tr key={group.id}>{group.headers.map((header) => <th key={header.id} scope="col">{flexRender(header.column.columnDef.header, header.getContext())}</th>)}</tr>)}</thead>
              <tbody>{table.getRowModel().rows.map((row) => <tr key={row.id}>{row.getVisibleCells().map((cell) => <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}</tr>)}</tbody>
            </table>
          </div>
        ) : null}

        {query.isSuccess && (cursorStack.length > 1 || query.data.has_more) ? (
          <nav className="pagination" aria-label="Audit pagination">
						<Button variant="secondary" disabled={cursorStack.length === 1 || query.isFetching} onClick={() => { setCursorStack((stack) => stack.slice(0, -1)); }}><ChevronLeft aria-hidden="true" size={17} />Previous</Button>
            <span>Page {cursorStack.length}</span>
						<Button variant="secondary" disabled={!query.data.has_more || !query.data.next_cursor || query.isFetching} onClick={() => { if (query.data.next_cursor) setCursorStack((stack) => [...stack, query.data.next_cursor ?? ""]); }}>Next<ChevronRight aria-hidden="true" size={17} /></Button>
          </nav>
        ) : null}
      </section>
    </div>
  );
}

function AuditTableSkeleton() {
  return <div className="table-skeleton" role="status" aria-label="Loading audit events">{Array.from({length: 6}, (_, index) => <span key={index} />)}</div>;
}

function outcomeTone(outcome: AuditEntry["outcome"]): "success" | "warning" | "danger" {
  if (outcome === "SUCCEEDED") return "success";
  if (outcome === "DENIED") return "warning";
  return "danger";
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(value));
}

function readable(value: string): string {
  return value.charAt(0) + value.slice(1).toLowerCase();
}

function errorMessage(error: Error): string {
  if (error instanceof APIError) return `${error.message} Reference: ${error.correlationID}`;
  return "The service is temporarily unavailable. Check your connection and try again.";
}
