import { z } from "zod";

import type { components } from "./schema.gen";

export type AdminSession = components["schemas"]["AdminSession"];
export type AuditEntry = components["schemas"]["AuditEntry"];
export type AuditPage = components["schemas"]["AuditPage"];
export type AdminOperationChange = components["schemas"]["AdminOperationChange"];
export type AdminOperationInput = components["schemas"]["AdminOperationInput"];
export type AdminOperationPage = components["schemas"]["AdminOperationPage"];

const roleSchema = z.enum(["SUPER_ADMIN", "COUNTRY_ADMIN", "CONTENT_ADMIN", "SUPPORT_ADMIN", "AUDITOR"]);
const navigationSchema = z.object({
  id: z.string().min(1).max(64),
  label: z.string().min(1).max(80),
  path: z.enum(["/", "/operations", "/audit"]),
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
const operationDomainSchema = z.enum(["CATALOG", "ORDER", "PAYMENT", "WALLET", "CAMPAIGN", "SUPPORT", "REPORTING"]);
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
