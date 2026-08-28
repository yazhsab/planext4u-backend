import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, ClipboardCheck, LockKeyhole, Plus, RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useOutletContext } from "react-router";
import { z } from "zod";

import {
  APIError,
  approveOperation,
  type AdminOperationChange,
  type AdminOperationInput,
  type AdminSession,
  listOperations,
  rejectOperation,
  submitOperation,
} from "../api/client";
import { Badge } from "../components/badge";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";

const domainSchema = z.enum(["CATALOG", "ORDER", "PAYMENT", "WALLET", "CAMPAIGN", "CMS", "SUPPORT", "REPORTING"]);
const operationSchema = z.object({
  domain: domainSchema,
  action: z.string().min(1),
  targetID: z.string().trim().min(1, "Enter the target identifier.").max(128).regex(/^[A-Za-z0-9._:-]+$/, "Use letters, numbers, dots, colons, underscores, or hyphens."),
  reason: z.string().trim().min(8, "Explain the verified reason in at least 8 characters.").max(500),
  payload: z.string().refine((value) => parsePayload(value) !== null, "Enter a JSON object without password, token, or secret fields."),
});
type OperationValues = z.infer<typeof operationSchema>;
type OperationDomain = OperationValues["domain"];

const domainCapabilities: Record<OperationDomain, string> = {
  CATALOG: "admin.catalog.manage",
  ORDER: "admin.order.manage",
  PAYMENT: "admin.payment.manage",
  WALLET: "admin.wallet.manage",
  CAMPAIGN: "admin.campaign.manage",
  CMS: "admin.config.manage",
  SUPPORT: "admin.support.manage",
  REPORTING: "admin.reporting.export",
};

const actionOptions: Record<OperationDomain, {value: string; label: string}[]> = {
  CATALOG: [{value: "UPSERT", label: "Save catalogue change"}, {value: "PUBLISH", label: "Publish"}, {value: "UNPUBLISH", label: "Unpublish"}, {value: "APPROVE", label: "Approve listing"}, {value: "REJECT", label: "Reject listing"}],
  ORDER: [{value: "CANCEL", label: "Cancel order"}, {value: "STATUS_OVERRIDE", label: "Override status"}, {value: "REFUND_APPROVE", label: "Approve refund"}],
  PAYMENT: [{value: "REFUND", label: "Issue refund"}, {value: "RECONCILE", label: "Reconcile payment"}, {value: "MARK_COD_COLLECTED", label: "Mark COD collected"}],
  WALLET: [{value: "ADJUST", label: "Adjust points"}, {value: "REVERSE", label: "Reverse entry"}, {value: "FREEZE", label: "Freeze wallet"}],
  CAMPAIGN: [{value: "UPSERT", label: "Save campaign"}, {value: "ACTIVATE", label: "Activate campaign"}, {value: "PAUSE", label: "Pause campaign"}],
  CMS: [{value: "UPSERT", label: "Save content revision"}, {value: "PUBLISH", label: "Publish content"}, {value: "ROLLBACK", label: "Roll back content"}],
  SUPPORT: [{value: "UPDATE_CASE", label: "Update case"}, {value: "ESCALATE", label: "Escalate case"}, {value: "RESOLVE", label: "Resolve case"}],
  REPORTING: [{value: "EXPORT", label: "Request export"}],
};

export function OperationsPage() {
  const session = useOutletContext<AdminSession>();
  const queryClient = useQueryClient();
  const availableDomains = useMemo(() => domainSchema.options.filter((domain) => session.capabilities.includes(domainCapabilities[domain])), [session.capabilities]);
  const query = useQuery({
    queryKey: ["admin", "operations", session.selected_country],
    queryFn: ({signal}) => listOperations(signal),
    enabled: session.capabilities.includes("admin.operations.read"),
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });

  if (!session.capabilities.includes("admin.operations.read")) {
    return <StatePanel icon={LockKeyhole} title="Operations access unavailable" message="Your administrator role cannot view privileged operations." />;
  }
  if (availableDomains.length === 0) {
    return <StatePanel icon={LockKeyhole} title="No operational domain assigned" message="Your role does not currently include a catalog, order, payment, wallet, campaign, CMS, support, or reporting capability." />;
  }

  return (
    <div className="page-stack">
      <header className="page-header">
        <div><p className="eyebrow">Controlled change</p><h1>Privileged operations</h1></div>
        <Badge tone={session.assurance.fresh_auth ? "success" : "warning"}>{session.assurance.fresh_auth ? "Fresh MFA" : "Re-authentication required"}</Badge>
      </header>
      <p className="page-intro">Submit country-scoped administrative changes. High-risk actions remain pending until a different authorized administrator approves them.</p>

      <OperationForm session={session} domains={availableDomains} onCreated={() => queryClient.invalidateQueries({queryKey: ["admin", "operations"]})} />

      <section className="table-section" aria-labelledby="operations-heading">
        <div className="section-heading">
          <div><h2 id="operations-heading">Change register</h2><p>Selected country: {session.selected_country}</p></div>
          <Button variant="quiet" disabled={query.isFetching} onClick={() => { void query.refetch(); }}><RefreshCw aria-hidden="true" size={16} />Refresh</Button>
        </div>
        {query.isPending ? <div className="table-skeleton" role="status" aria-label="Loading privileged operations">{Array.from({length: 4}, (_, index) => <span key={index} />)}</div> : null}
        {query.isError ? <StatePanel icon={AlertTriangle} tone="danger" title="Operations could not be loaded" message={operationErrorMessage(query.error)} actionLabel="Try again" onAction={() => { void query.refetch(); }} /> : null}
        {query.data?.changes.length === 0 ? <StatePanel icon={ClipboardCheck} title="No changes in this country" message="Submitted operations will appear here without exposing other roles or countries." /> : null}
        {query.data && query.data.changes.length > 0 ? <OperationsTable changes={query.data.changes} session={session} /> : null}
      </section>
    </div>
  );
}

function OperationForm({session, domains, onCreated}: {session: AdminSession; domains: OperationDomain[]; onCreated: () => Promise<unknown>}) {
  const initialDomain = domains[0] ?? "CATALOG";
  const {register, handleSubmit, watch, setValue, reset, formState: {errors}} = useForm<OperationValues>({
    resolver: zodResolver(operationSchema),
    defaultValues: {domain: initialDomain, action: actionOptions[initialDomain][0]?.value ?? "", targetID: "", reason: "", payload: "{}"},
  });
  const selectedDomain = watch("domain");
  const mutation = useMutation({
    mutationFn: (input: AdminOperationInput) => submitOperation(input, session.csrf_token),
    onSuccess: async () => {
      reset({domain: selectedDomain, action: actionOptions[selectedDomain][0]?.value ?? "", targetID: "", reason: "", payload: "{}"});
      await onCreated();
    },
  });

  useEffect(() => {
    setValue("action", actionOptions[selectedDomain][0]?.value ?? "");
  }, [selectedDomain, setValue]);

  const submit = handleSubmit((values) => {
    const payload = parsePayload(values.payload);
    if (payload === null) return;
    mutation.mutate({domain: values.domain, action: values.action, target_id: values.targetID, reason: values.reason, payload});
  });

  return (
    <section className="operation-form-section" aria-labelledby="new-operation-heading">
      <div className="operation-form-intro"><span className="detail-icon"><Plus aria-hidden="true" /></span><div><h2 id="new-operation-heading">New controlled change</h2><p>Payload values are validated by the owning service. Credentials and secrets are rejected.</p></div></div>
      <form className="operation-form" onSubmit={(event) => { void submit(event); }} noValidate>
        <Field label="Domain" error={errors.domain?.message}><select {...register("domain")}>{domains.map((domain) => <option key={domain} value={domain}>{readable(domain)}</option>)}</select></Field>
        <Field label="Action" error={errors.action?.message}><select {...register("action")}>{actionOptions[selectedDomain].map((action) => <option key={action.value} value={action.value}>{action.label}</option>)}</select></Field>
        <Field label="Target identifier" error={errors.targetID?.message}><input {...register("targetID")} autoComplete="off" placeholder="order-001" /></Field>
        <Field label="Verified reason" error={errors.reason?.message} wide><textarea {...register("reason")} rows={3} placeholder="Explain why this change is required and what was verified." /></Field>
        <Field label="Operation payload (JSON object)" error={errors.payload?.message} wide><textarea {...register("payload")} className="code-input" rows={4} spellCheck={false} /></Field>
        <div className="operation-submit">
          <Button type="submit" disabled={mutation.isPending || !session.assurance.fresh_auth}>{mutation.isPending ? "Submitting…" : "Submit controlled change"}</Button>
          {!session.assurance.fresh_auth ? <a href="/login?reauth=mfa">Re-authenticate before submitting</a> : null}
          {mutation.isError ? <p role="alert">{operationErrorMessage(mutation.error)}</p> : null}
          {mutation.isSuccess ? <p role="status"><CheckCircle2 aria-hidden="true" size={16} />Change {mutation.data.status === "PENDING_APPROVAL" ? "submitted for independent approval" : "executed"}.</p> : null}
        </div>
      </form>
    </section>
  );
}

function Field({label, error, wide = false, children}: {label: string; error?: string; wide?: boolean; children: React.ReactNode}) {
  const id = label.toLowerCase().replaceAll(/[^a-z0-9]+/g, "-");
  return <label className={wide ? "operation-field operation-field-wide" : "operation-field"}><span>{label}</span>{children}{error ? <small id={`${id}-error`} role="alert">{error}</small> : null}</label>;
}

function OperationsTable({changes, session}: {changes: AdminOperationChange[]; session: AdminSession}) {
  return (
    <div className="table-scroll" tabIndex={0} aria-label="Scrollable privileged operations table">
      <table>
        <thead><tr><th scope="col">Operation</th><th scope="col">Target</th><th scope="col">Risk</th><th scope="col">Status</th><th scope="col">Requested by</th><th scope="col">Updated</th><th scope="col">Approval</th></tr></thead>
        <tbody>{changes.map((change) => <tr key={change.id}>
          <td><span className="primary-cell">{readable(change.command.domain)}</span><br /><code>{change.command.action}</code></td>
          <td><code>{change.command.target_id}</code></td>
          <td><Badge tone={change.risk === "HIGH" ? "warning" : "neutral"}>{readable(change.risk)}</Badge></td>
          <td><Badge tone={change.status === "EXECUTED" ? "success" : change.status === "REJECTED" ? "danger" : "warning"}>{readable(change.status)}</Badge></td>
          <td><code>{change.requested_by}</code></td>
          <td>{new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(change.updated_at))}</td>
          <td>{change.status === "PENDING_APPROVAL" ? <PendingActions change={change} session={session} /> : <span className="quiet-status">{change.approved_by ? `By ${change.approved_by}` : "No action required"}</span>}</td>
        </tr>)}</tbody>
      </table>
    </div>
  );
}

function PendingActions({change, session}: {change: AdminOperationChange; session: AdminSession}) {
  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");
  const approve = useMutation({mutationFn: () => approveOperation(change, session.csrf_token), onSuccess: () => queryClient.invalidateQueries({queryKey: ["admin", "operations"]})});
  const reject = useMutation({mutationFn: () => rejectOperation(change, reason, session.csrf_token), onSuccess: () => queryClient.invalidateQueries({queryKey: ["admin", "operations"]})});
  if (change.requested_by === session.subject_id) return <span className="quiet-status">Awaiting another administrator</span>;
  return <details className="approval-actions"><summary>Review</summary><div><Button type="button" onClick={() => { approve.mutate(); }} disabled={!session.assurance.fresh_auth || approve.isPending || reject.isPending}>Approve</Button><label><span>Rejection reason</span><input value={reason} onChange={(event) => { setReason(event.target.value); }} minLength={8} maxLength={500} /></label><Button type="button" variant="secondary" onClick={() => { reject.mutate(); }} disabled={reason.trim().length < 8 || !session.assurance.fresh_auth || approve.isPending || reject.isPending}>Reject</Button>{approve.isError || reject.isError ? <p role="alert">{operationErrorMessage(approve.error ?? reject.error)}</p> : null}</div></details>;
}

function parsePayload(value: string): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(value);
    if (!isRecord(parsed) || containsSensitiveField(parsed)) return null;
    return parsed;
  } catch {
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function containsSensitiveField(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(containsSensitiveField);
  if (!isRecord(value)) return false;
  return Object.entries(value).some(([key, child]) => /password|token|secret/i.test(key) || containsSensitiveField(child));
}

function operationErrorMessage(error: Error | null): string {
  return error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "The operation could not be completed. Check your connection and try again.";
}

function readable(value: string): string {
  return value.toLowerCase().split("_").map((word) => (word[0]?.toUpperCase() ?? "") + word.slice(1)).join(" ");
}
