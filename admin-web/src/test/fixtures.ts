import type { AdminOperationPage, AdminSession, AdminSupportTicketPage, AuditPage, CMSPageDraftPage, CMSWorkspaceDraft, ReportDetail, ReportExport, ReportPage } from "../api/client";

export const adminSession: AdminSession = {
  subject_id: "admin-synthetic-001",
  display_name: "Asha Raman",
  roles: ["COUNTRY_ADMIN"],
  capabilities: ["admin.audit.read", "admin.campaign.manage", "admin.catalog.manage", "admin.config.manage", "admin.content.manage", "admin.country.manage", "admin.dispatch.manage", "admin.emergency.manage", "admin.franchise.manage", "admin.governance.read", "admin.intelligence.manage", "admin.operations.read", "admin.order.manage", "admin.payment.manage", "admin.policy.manage", "admin.reporting.export", "admin.restaurant.manage", "admin.settlement.manage", "admin.shell.read", "admin.supply.manage", "admin.support.manage", "admin.wallet.manage"],
  allowed_countries: ["IN", "SG"],
  selected_country: "IN",
  assurance: {mfa_satisfied: true, fresh_auth: true, auth_time: "2026-08-27T06:30:00Z"},
  navigation: [
    {id: "workspace", label: "Workspace", path: "/", capability: "admin.shell.read"},
    {id: "operations", label: "Operations", path: "/operations", capability: "admin.operations.read"},
    {id: "governance", label: "Governance", path: "/governance", capability: "admin.governance.read"},
    {id: "support", label: "Support", path: "/support", capability: "admin.support.manage"},
    {id: "cms", label: "Page builder", path: "/cms", capability: "admin.config.manage"},
    {id: "audit", label: "Audit trail", path: "/audit", capability: "admin.audit.read"},
  ],
  csrf_token: "csrf_synthetic_012345678901234567890123456789",
};

export const supportTicketPage: AdminSupportTicketPage = {
  items: [{
    id: "2c231280-3cf4-49be-9e6c-96683f4de86a",
    owner_role: "VENDOR",
    owner_reference: "vendor-synthetic-001",
    category: "VENDOR_OPERATIONS",
    subject: "Catalogue review needs assistance",
    related_reference: "item-synthetic-001",
    priority: "HIGH",
    status: "WAITING_FOR_SUPPORT",
    message_count: 2,
    last_message_at: "2026-09-01T10:01:00Z",
    created_at: "2026-09-01T10:00:00Z",
    updated_at: "2026-09-01T10:01:00Z",
  }],
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

export const governanceView = {
  country: "IN",
  policy_version: "policy-IN-2026.08",
  feature_flags: {socio: true, homes: true, classifieds: true, emergency: true},
  metrics: [
    {id: "social-active", title: "Socio active", value: 5600, unit: "count", freshness: "2026-08-29T10:00:00Z", masked: true},
    {id: "emergency-sla", title: "Emergency within SLA", value: 99, unit: "percent", freshness: "2026-08-29T10:00:00Z", masked: true},
  ],
  privacy_mode: "aggregate_and_masked" as const,
  generated_at: "2026-08-29T10:00:00Z",
};

export const reportPage: ReportPage = {
  items: governanceView.metrics.map((metric) => ({
    ...metric,
    domain: metric.id.split("-")[0] === "social" ? "social" : "emergency",
    metric: metric.id.split("-").slice(1).join("_"),
    export_policy: "MFA_AND_AUDIT_REQUIRED" as const,
  })),
};

export const reportDetail: ReportDetail = {
  report: {
    id: "social-active",
    title: "Socio active",
    domain: "social",
    metric: "active",
    value: 5600,
    unit: "count",
    freshness: "2026-08-29T10:00:00Z",
    masked: true,
    export_policy: "MFA_AND_AUDIT_REQUIRED",
  },
  country: "IN",
  items: [{label: "Socio active", dimensions: {country: "IN", domain: "social", metric: "active"}, value: 5600, unit: "count"}],
  lineage: {source_projection: "governance.report_cards", aggregation: "country_aggregate", freshness: "2026-08-29T10:00:00Z", generated_at: "2026-08-29T10:00:00Z"},
};

export const reportExport: ReportExport = {
  id: "report-export-0123456789abcdef0123456789abcdef",
  report_id: "social-active",
  format: "CSV",
  status: "READY",
  content_type: "text/csv; charset=utf-8",
  file_name: "social-active-in.csv",
  checksum_sha256: "a".repeat(64),
  size_bytes: 128,
  download_url: "/admin/api/v1/report-exports/report-export-0123456789abcdef0123456789abcdef/download?token=synthetic-signed-token-0123456789",
  expires_at: "2026-08-29T10:10:00Z",
  created_at: "2026-08-29T10:00:00Z",
};

export const cmsPageDraftPage: CMSPageDraftPage = {
  items: [{
    tenant_id: "tenant-synthetic-001",
    country: "IN",
    revision: 3,
    updated_by: "admin-synthetic-001",
    updated_at: "2026-08-29T10:00:00Z",
    page: {
      id: "customer-home",
      route: "/app",
      title_key: "Customer home",
      audience: ["PUBLIC", "CUSTOMER"],
      enabled: true,
      blocks: [
        {id: "home-hero", kind: "HERO", title_key: "Welcome", enabled: true, priority: 0, content: {headline: "Smart shopping, everyday."}},
        {id: "home-products", kind: "ITEM_RAIL", title_key: "Bestsellers", enabled: true, priority: 1, content: {collection_id: "bestsellers", limit: 8}},
      ],
    },
  }],
};

export const cmsWorkspaceDraft: CMSWorkspaceDraft = {
  tenant_id: "tenant-synthetic-001",
  country: "IN",
  revision: 4,
  updated_by: "admin-synthetic-001",
  updated_at: "2026-08-29T10:00:00Z",
  workspace: {
    minimum_versions: {ANDROID: "1.0.0", IOS: "1.0.0", WEB: "1.0.0"},
    latest_versions: {ANDROID: "1.2.0", IOS: "1.2.0", WEB: "1.2.0"},
    supported_locales: ["en", "ta"],
    default_locale: "en",
    consent_policies: [{purpose: "analytics", policy_version: "2026-08", required: false}],
    flags: {shop: true, services: true, homes: true, classifieds: true, socio: true},
    home_sections: [
      {id: "hero", kind: "HERO", title_key: "Home hero", enabled: true, priority: 0},
      {id: "products", kind: "ITEM_RAIL", title_key: "Bestsellers", enabled: true, priority: 1},
    ],
  },
};

export function problem(status: number, code: string, message: string): Response {
  return Response.json({error: {code, message, correlation_id: "corr-synthetic-problem", retryable: status >= 500, field_errors: [], details: {}}}, {status});
}
