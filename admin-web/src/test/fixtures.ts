import type { AdminOperationPage, AdminSession, AuditPage } from "../api/client";

export const adminSession: AdminSession = {
  subject_id: "admin-synthetic-001",
  display_name: "Asha Raman",
  roles: ["COUNTRY_ADMIN"],
  capabilities: ["admin.audit.read", "admin.campaign.manage", "admin.catalog.manage", "admin.config.manage", "admin.content.manage", "admin.dispatch.manage", "admin.franchise.manage", "admin.operations.read", "admin.order.manage", "admin.payment.manage", "admin.reporting.export", "admin.restaurant.manage", "admin.settlement.manage", "admin.shell.read", "admin.supply.manage", "admin.support.manage", "admin.wallet.manage"],
  allowed_countries: ["IN", "SG"],
  selected_country: "IN",
  assurance: {mfa_satisfied: true, fresh_auth: true, auth_time: "2026-08-27T06:30:00Z"},
  navigation: [
    {id: "workspace", label: "Workspace", path: "/", capability: "admin.shell.read"},
    {id: "operations", label: "Operations", path: "/operations", capability: "admin.operations.read"},
    {id: "audit", label: "Audit trail", path: "/audit", capability: "admin.audit.read"},
  ],
  csrf_token: "csrf_synthetic_012345678901234567890123456789",
};

export const operationPage: AdminOperationPage = {
  changes: [{
    id: "change-synthetic-001",
    revision: 1,
    tenant_id: "tenant-synthetic-001",
    country: "IN",
    command: {domain: "WALLET", action: "ADJUST", target_id: "customer-synthetic-001", reason: "Correct verified settlement discrepancy", payload: {points: 100}, correlation_id: "corr-synthetic-operation-001"},
    risk: "HIGH",
    status: "PENDING_APPROVAL",
    requested_by: "admin-requester-001",
    created_at: "2026-08-27T06:40:00Z",
    updated_at: "2026-08-27T06:40:00Z",
  }],
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
