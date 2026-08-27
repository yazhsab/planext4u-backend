import type { AdminSession, AuditPage } from "../api/client";

export const adminSession: AdminSession = {
  subject_id: "admin-synthetic-001",
  display_name: "Asha Raman",
  roles: ["COUNTRY_ADMIN"],
  capabilities: ["admin.audit.read", "admin.config.manage", "admin.content.manage", "admin.shell.read", "admin.support.manage"],
  allowed_countries: ["IN", "SG"],
  selected_country: "IN",
  assurance: {mfa_satisfied: true, fresh_auth: true, auth_time: "2026-08-27T06:30:00Z"},
  navigation: [
    {id: "workspace", label: "Workspace", path: "/", capability: "admin.shell.read"},
    {id: "audit", label: "Audit trail", path: "/audit", capability: "admin.audit.read"},
  ],
  csrf_token: "csrf_synthetic_012345678901234567890123456789",
};

export const auditPage: AuditPage = {
  entries: [{
    id: "audit-synthetic-001",
    tenant_id: "tenant-synthetic-001",
    country: "IN",
    actor: {subject_id: "admin-synthetic-001", actor_type: "USER"},
    action: "admin.session.opened",
    target: {type: "session", id: "session-synthetic-001"},
    outcome: "SUCCEEDED",
    reason_code: "SESSION_OPENED",
    correlation_id: "corr-synthetic-admin-001",
    occurred_at: "2026-08-27T06:30:00Z",
    recorded_at: "2026-08-27T06:30:01Z",
    hash: "a".repeat(64),
    sequence: 1,
  }],
  has_more: false,
};

export function problem(status: number, code: string, message: string): Response {
  return Response.json({error: {code, message, correlation_id: "corr-synthetic-problem", retryable: status >= 500, field_errors: [], details: {}}}, {status});
}
