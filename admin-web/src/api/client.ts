import { z } from "zod";

import type { components } from "./schema.gen";

export type AdminSession = components["schemas"]["AdminSession"];
export type AuditEntry = components["schemas"]["AuditEntry"];
export type AuditPage = components["schemas"]["AuditPage"];
export type AdminOperationChange = components["schemas"]["AdminOperationChange"];
export type AdminOperationInput = components["schemas"]["AdminOperationInput"];
export type AdminOperationPage = components["schemas"]["AdminOperationPage"];
export type CMSPage = components["schemas"]["Page"];
export type CMSPageBlock = components["schemas"]["PageBlock"];
export type CMSPageDraft = components["schemas"]["CMSPageDraft"];
export type CMSPageDraftPage = components["schemas"]["CMSPageDraftPage"];
export type CMSWorkspace = components["schemas"]["Workspace"];
export type CMSWorkspaceDraft = components["schemas"]["CMSWorkspaceDraft"];
export type AdminSupportTicket = components["schemas"]["AdminTicket"];
export type AdminSupportTicketPage = components["schemas"]["AdminTicketPage"];
export type ReportSummary = components["schemas"]["ReportSummary"];
export type ReportPage = components["schemas"]["ReportPage"];
export type ReportDetail = components["schemas"]["ReportDetail"];
export type ReportExport = components["schemas"]["ReportExport"];
export type CreateReportExportRequest = components["schemas"]["CreateReportExportRequest"];

const roleSchema = z.enum(["SUPER_ADMIN", "COUNTRY_ADMIN", "CONTENT_ADMIN", "SUPPORT_ADMIN", "AUDITOR"]);
const navigationSchema = z.object({
  id: z.string().min(1).max(64),
  label: z.string().min(1).max(80),
  path: z.enum(["/", "/operations", "/governance", "/support", "/cms", "/audit"]),
  capability: z.string().min(1).max(128),
});
const sessionSchema = z.object({
  subject_id: z.string().min(1).max(128),
  display_name: z.string().min(1).max(128),
  roles: z.array(roleSchema).min(1).max(8),
  capabilities: z.array(z.string().min(1).max(128)).max(64),
  allowed_countries: z.array(z.string().regex(/^[A-Z]{2}$/)).min(1).max(32),
  selected_country: z.string().regex(/^[A-Z]{2}$/),
  assurance: z.object({
    mfa_satisfied: z.boolean(),
    fresh_auth: z.boolean(),
    auth_time: z.iso.datetime({offset: true}),
  }),
  navigation: z.array(navigationSchema).min(1).max(16),
  csrf_token: z.string().min(32).max(256),
});
const auditEntrySchema = z.object({
  id: z.string().min(1).max(128),
  tenant_id: z.string().min(1).max(128),
  country: z.string().regex(/^[A-Z]{2}$/),
  actor: z.object({subject_id: z.string().min(1).max(128), actor_type: z.enum(["USER", "SERVICE", "SYSTEM"]), ip_prefix: z.string().optional()}),
  action: z.string().min(1).max(128),
  target: z.object({type: z.string().min(1).max(128), id: z.string().min(1).max(128)}),
  outcome: z.enum(["SUCCEEDED", "DENIED", "FAILED"]),
  reason_code: z.string().min(1).max(64),
  correlation_id: z.string().min(1).max(128),
  occurred_at: z.iso.datetime({offset: true}),
  recorded_at: z.iso.datetime({offset: true}),
  hash: z.string().regex(/^[a-f0-9]{64}$/),
  sequence: z.number().int().positive(),
});
const auditPageSchema = z.object({
  entries: z.array(auditEntrySchema),
  next_cursor: z.string().optional(),
  has_more: z.boolean(),
});
const operationDomainSchema = z.enum(["CATALOG", "ORDER", "PAYMENT", "WALLET", "CAMPAIGN", "CMS", "SUPPORT", "REPORTING", "SUPPLY", "RESTAURANT", "DISPATCH", "SETTLEMENT", "FRANCHISE", "CONTENT", "POLICY", "COUNTRY", "EMERGENCY", "INTELLIGENCE"]);
const operationInputSchema = z.object({
  domain: operationDomainSchema,
  action: z.string().min(1).max(128),
  target_id: z.string().min(1).max(128),
  reason: z.string().min(8).max(500),
  payload: z.record(z.string(), z.unknown()),
});
const operationChangeSchema = z.object({
  id: z.string().min(1).max(128),
  revision: z.number().int().positive(),
  tenant_id: z.string().min(1).max(128),
  country: z.string().regex(/^[A-Z]{2}$/),
  command: operationInputSchema.extend({correlation_id: z.string().min(1).max(128)}),
  risk: z.enum(["STANDARD", "HIGH"]),
  status: z.enum(["PENDING_APPROVAL", "EXECUTED", "REJECTED"]),
  requested_by: z.string().min(1).max(128),
  approved_by: z.string().min(1).max(128).optional(),
  created_at: z.iso.datetime({offset: true}),
  updated_at: z.iso.datetime({offset: true}),
});
const operationPageSchema = z.object({changes: z.array(operationChangeSchema)});
const governanceSchema = z.object({
  country: z.string().regex(/^[A-Z]{2}$/),
  policy_version: z.string().min(1).max(128),
  feature_flags: z.record(z.string(), z.boolean()),
  metrics: z.array(z.object({id: z.string(), title: z.string(), value: z.number().int(), unit: z.string(), freshness: z.iso.datetime({offset: true}), masked: z.boolean()})),
  privacy_mode: z.literal("aggregate_and_masked"),
  generated_at: z.iso.datetime({offset: true}),
});
const reportSummarySchema = z.object({
  id: z.string().min(1).max(128),
  title: z.string().min(1).max(240),
  domain: z.string().min(1).max(128),
  metric: z.string().min(1).max(128),
  value: z.number().int(),
  unit: z.string().min(1).max(128),
  freshness: z.iso.datetime({offset: true}),
  masked: z.boolean(),
  export_policy: z.literal("MFA_AND_AUDIT_REQUIRED"),
});
const reportPageSchema = z.object({items: z.array(reportSummarySchema).max(50), next_cursor: z.string().max(512).optional()});
const reportDetailSchema = z.object({
  report: reportSummarySchema,
  country: z.string().regex(/^[A-Z]{2}$/),
  from: z.iso.datetime({offset: true}).optional(),
  to: z.iso.datetime({offset: true}).optional(),
  items: z.array(z.object({
    label: z.string(),
    dimensions: z.record(z.string(), z.string()),
    value: z.number().int(),
    unit: z.string(),
  })).max(50),
  next_cursor: z.string().max(512).optional(),
  lineage: z.object({
    source_projection: z.string(),
    aggregation: z.string(),
    freshness: z.iso.datetime({offset: true}),
    generated_at: z.iso.datetime({offset: true}),
  }),
});
const reportExportSchema = z.object({
  id: z.string().regex(/^report-export-[a-f0-9]{32}$/),
  report_id: z.string().min(1).max(128),
  format: z.literal("CSV"),
  status: z.enum(["PROCESSING", "READY", "FAILED"]),
  content_type: z.literal("text/csv; charset=utf-8").optional(),
  file_name: z.string().regex(/^[A-Za-z0-9._:-]+\.csv$/).optional(),
  checksum_sha256: z.string().regex(/^[a-f0-9]{64}$/).optional(),
  size_bytes: z.number().int().positive().optional(),
  download_url: z.string().regex(/^\/admin\/api\/v1\/report-exports\//).optional(),
  expires_at: z.iso.datetime({offset: true}),
  created_at: z.iso.datetime({offset: true}),
});
const supportRoleSchema = z.enum(["CUSTOMER", "VENDOR", "RIDER"]);
const supportStatusSchema = z.enum(["OPEN", "WAITING_FOR_SUPPORT", "WAITING_FOR_REQUESTER", "RESOLVED", "CLOSED"]);
const supportTicketSchema = z.object({
  id: z.uuid(),
  owner_role: supportRoleSchema,
  owner_reference: z.string().min(1).max(128),
  category: z.enum(["ACCOUNT", "ORDER", "PAYMENT", "VENDOR_OPERATIONS", "RIDER_OPERATIONS", "OTHER"]),
  subject: z.string().min(4).max(160),
  related_reference: z.string().min(1).max(128).optional(),
  priority: z.enum(["LOW", "NORMAL", "HIGH", "URGENT"]),
  status: supportStatusSchema,
  message_count: z.number().int().positive(),
  last_message_at: z.iso.datetime({offset: true}),
  created_at: z.iso.datetime({offset: true}),
  updated_at: z.iso.datetime({offset: true}),
});
const supportTicketPageSchema = z.object({
  items: z.array(supportTicketSchema).max(100),
  next_cursor: z.string().max(512).optional(),
});
const pageBlockSchema = z.object({
  id: z.string().min(1).max(128),
  kind: z.string().min(1).max(64),
  title_key: z.string().min(1).max(160).optional(),
  enabled: z.boolean(),
  priority: z.number().int().nonnegative(),
  content: z.record(z.string(), z.unknown()),
});
const pageSchema = z.object({
  id: z.string().min(1).max(128),
  route: z.string().startsWith("/").max(256),
  title_key: z.string().min(1).max(160),
  audience: z.array(z.enum(["PUBLIC", "CUSTOMER", "VENDOR", "RIDER"])).min(1).max(4),
  enabled: z.boolean(),
  blocks: z.array(pageBlockSchema).max(128),
});
const cmsPageDraftSchema = z.object({
  tenant_id: z.string().min(1).max(128),
  country: z.string().regex(/^[A-Z]{2}$/),
  page: pageSchema,
  revision: z.number().int().positive(),
  updated_by: z.string().min(1).max(128),
  updated_at: z.iso.datetime({offset: true}),
});
const cmsPageDraftPageSchema = z.object({items: z.array(cmsPageDraftSchema)});
const semanticVersionSchema = z.string().regex(/^\d+\.\d+\.\d+$/);
const workspaceSchema = z.object({
  minimum_versions: z.object({ANDROID: semanticVersionSchema, IOS: semanticVersionSchema, WEB: semanticVersionSchema}),
  latest_versions: z.object({ANDROID: semanticVersionSchema, IOS: semanticVersionSchema, WEB: semanticVersionSchema}),
  supported_locales: z.array(z.enum(["en", "ta", "hi", "te", "kn", "ml", "mr", "bn", "gu"])).min(1).max(9),
  default_locale: z.enum(["en", "ta", "hi", "te", "kn", "ml", "mr", "bn", "gu"]),
  consent_policies: z.array(z.object({
    purpose: z.string().min(1).max(128),
    policy_version: z.string().min(1).max(128),
    required: z.boolean(),
  })).max(32),
  flags: z.record(z.string(), z.boolean()),
  home_sections: z.array(z.object({
    id: z.string().min(1).max(128),
    kind: z.string().min(1).max(64),
    title_key: z.string().min(1).max(160),
    enabled: z.boolean(),
    priority: z.number().int(),
  })).max(64),
  maintenance_window: z.object({
    starts_at: z.iso.datetime({offset: true}),
    ends_at: z.iso.datetime({offset: true}),
    message: z.string().min(1).max(240),
  }).optional(),
});
const cmsWorkspaceDraftSchema = z.object({
  tenant_id: z.string().min(1).max(128),
  country: z.string().regex(/^[A-Z]{2}$/),
  workspace: workspaceSchema,
  revision: z.number().int().positive(),
  updated_by: z.string().min(1).max(128),
  updated_at: z.iso.datetime({offset: true}),
});
export type GovernanceView = z.infer<typeof governanceSchema>;
const problemSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    correlation_id: z.string(),
    retryable: z.boolean(),
  }),
});

export class APIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly correlationID: string;

  constructor(status: number, code: string, message: string, correlationID = "unavailable") {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
    this.correlationID = correlationID;
  }
}

async function requestJSON<T>(path: string, schema: z.ZodType<T>, init?: RequestInit): Promise<T> {
	const headers = new Headers(init?.headers);
	if (!headers.has("Accept")) headers.set("Accept", "application/json");
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
		headers,
  });
  if (!response.ok) {
    const parsed = problemSchema.safeParse(await response.json().catch(() => null));
    if (parsed.success) {
      throw new APIError(response.status, parsed.data.error.code, parsed.data.error.message, parsed.data.error.correlation_id);
    }
    throw new APIError(response.status, "ADMIN_RESPONSE_INVALID", "The administration service returned an unreadable response.");
  }
  const parsed = schema.safeParse(await response.json());
  if (!parsed.success) {
    throw new APIError(response.status, "ADMIN_CONTRACT_INVALID", "The administration service response did not match its contract.");
  }
  return parsed.data;
}

export function getSession(signal?: AbortSignal): Promise<AdminSession> {
  return requestJSON("/admin/api/v1/session", sessionSchema, {signal});
}

export async function setCountry(country: string, csrfToken: string): Promise<void> {
  const response = await fetch("/admin/api/v1/session/country", {
    method: "PUT",
    credentials: "same-origin",
    headers: {"Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrfToken},
    body: JSON.stringify({country}),
  });
  if (!response.ok) {
    const parsed = problemSchema.safeParse(await response.json().catch(() => null));
    throw new APIError(
      response.status,
      parsed.success ? parsed.data.error.code : "ADMIN_RESPONSE_INVALID",
      parsed.success ? parsed.data.error.message : "The country context could not be changed.",
      parsed.success ? parsed.data.error.correlation_id : "unavailable",
    );
  }
}

export type AuditFilters = {action?: string; cursor?: string; limit?: number};

export function listAuditEvents(filters: AuditFilters, signal?: AbortSignal): Promise<AuditPage> {
  const query = new URLSearchParams();
  if (filters.action) query.set("action", filters.action);
  if (filters.cursor) query.set("cursor", filters.cursor);
  query.set("limit", String(filters.limit ?? 50));
  return requestJSON(`/admin/api/v1/audit/events?${query.toString()}`, auditPageSchema, {signal});
}

export function listOperations(signal?: AbortSignal): Promise<AdminOperationPage> {
  return requestJSON("/admin/api/v1/operations", operationPageSchema, {signal});
}

export function getGovernance(signal?: AbortSignal): Promise<GovernanceView> {
  return requestJSON("/admin/api/v1/governance", governanceSchema, {signal});
}

export type ReportFilters = {domain?: string; from?: string; to?: string; cursor?: string; limit?: number};

export function listReports(filters: Pick<ReportFilters, "domain" | "cursor" | "limit"> = {}, signal?: AbortSignal): Promise<ReportPage> {
  const query = new URLSearchParams();
  if (filters.domain) query.set("domain", filters.domain);
  if (filters.cursor) query.set("cursor", filters.cursor);
  query.set("limit", String(filters.limit ?? 20));
  return requestJSON(`/admin/api/v1/reports?${query.toString()}`, reportPageSchema, {signal});
}

export function getReportDetail(reportID: string, filters: Pick<ReportFilters, "from" | "to" | "cursor" | "limit"> = {}, signal?: AbortSignal): Promise<ReportDetail> {
  const query = new URLSearchParams();
  if (filters.from) query.set("from", filters.from);
  if (filters.to) query.set("to", filters.to);
  if (filters.cursor) query.set("cursor", filters.cursor);
  query.set("limit", String(filters.limit ?? 20));
  return requestJSON(`/admin/api/v1/reports/${encodeURIComponent(reportID)}?${query.toString()}`, reportDetailSchema, {signal});
}

export function createReportExport(reportID: string, input: CreateReportExportRequest, csrfToken: string): Promise<ReportExport> {
  return requestJSON(`/admin/api/v1/reports/${encodeURIComponent(reportID)}/exports`, reportExportSchema, {
    method: "POST",
    headers: {"Content-Type": "application/json", "X-CSRF-Token": csrfToken, "X-Correlation-ID": `admin-report-${globalThis.crypto.randomUUID()}`},
    body: JSON.stringify(input),
  });
}

export function getReportExport(exportID: string, signal?: AbortSignal): Promise<ReportExport> {
  return requestJSON(`/admin/api/v1/report-exports/${encodeURIComponent(exportID)}`, reportExportSchema, {signal});
}

export type SupportTicketFilters = {
  ownerRole?: "CUSTOMER" | "VENDOR" | "RIDER";
  status?: "OPEN" | "WAITING_FOR_SUPPORT" | "WAITING_FOR_REQUESTER" | "RESOLVED" | "CLOSED";
  cursor?: string;
  limit?: number;
};

export function listSupportTickets(filters: SupportTicketFilters = {}, signal?: AbortSignal): Promise<AdminSupportTicketPage> {
  const query = new URLSearchParams();
  if (filters.ownerRole) query.set("owner_role", filters.ownerRole);
  if (filters.status) query.set("status", filters.status);
  if (filters.cursor) query.set("cursor", filters.cursor);
  query.set("limit", String(filters.limit ?? 50));
  return requestJSON(`/admin/api/v1/support/tickets?${query.toString()}`, supportTicketPageSchema, {signal});
}

export function listCMSPageDrafts(signal?: AbortSignal): Promise<CMSPageDraftPage> {
  return requestJSON("/admin/api/v1/cms/pages", cmsPageDraftPageSchema, {signal});
}

export function saveCMSPageDraft(pageID: string, expectedRevision: number, page: CMSPage, csrfToken: string): Promise<CMSPageDraft> {
  return requestJSON(`/admin/api/v1/cms/pages/${encodeURIComponent(pageID)}/draft`, cmsPageDraftSchema, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrfToken,
      "X-Correlation-ID": `admin-web-${globalThis.crypto.randomUUID()}`,
    },
    body: JSON.stringify({expected_revision: expectedRevision, page}),
  });
}

export function getCMSWorkspace(signal?: AbortSignal): Promise<CMSWorkspaceDraft> {
  return requestJSON("/admin/api/v1/cms/workspace", cmsWorkspaceDraftSchema, {signal});
}

export function saveCMSWorkspace(expectedRevision: number, workspace: CMSWorkspace, csrfToken: string): Promise<CMSWorkspaceDraft> {
  return requestJSON("/admin/api/v1/cms/workspace/draft", cmsWorkspaceDraftSchema, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrfToken,
      "X-Correlation-ID": `admin-web-${globalThis.crypto.randomUUID()}`,
    },
    body: JSON.stringify({expected_revision: expectedRevision, workspace}),
  });
}

export function submitOperation(input: AdminOperationInput, csrfToken: string): Promise<AdminOperationChange> {
  return operationMutation("/admin/api/v1/operations", input, csrfToken);
}

export function approveOperation(change: AdminOperationChange, csrfToken: string): Promise<AdminOperationChange> {
  return operationMutation(`/admin/api/v1/operations/${encodeURIComponent(change.id)}/approve`, {expected_revision: change.revision}, csrfToken);
}

export function rejectOperation(change: AdminOperationChange, reason: string, csrfToken: string): Promise<AdminOperationChange> {
  return operationMutation(`/admin/api/v1/operations/${encodeURIComponent(change.id)}/reject`, {expected_revision: change.revision, reason}, csrfToken);
}

function operationMutation(path: string, body: unknown, csrfToken: string): Promise<AdminOperationChange> {
  return requestJSON(path, operationChangeSchema, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrfToken,
      "X-Correlation-ID": `admin-web-${globalThis.crypto.randomUUID()}`,
    },
    body: JSON.stringify(body),
  });
}
