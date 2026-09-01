import { createServer } from "node:http";

const session = {
  subject_id: "admin-synthetic-001",
  display_name: "Asha Raman",
  roles: ["COUNTRY_ADMIN"],
  capabilities: ["admin.audit.read", "admin.campaign.manage", "admin.catalog.manage", "admin.config.manage", "admin.content.manage", "admin.operations.read", "admin.order.manage", "admin.payment.manage", "admin.reporting.export", "admin.shell.read", "admin.support.manage", "admin.wallet.manage"],
  allowed_countries: ["IN", "SG"],
  selected_country: "IN",
  assurance: {mfa_satisfied: true, fresh_auth: true, auth_time: "2026-08-27T06:30:00Z"},
  navigation: [
    {id: "workspace", label: "Workspace", path: "/", capability: "admin.shell.read"},
    {id: "operations", label: "Operations", path: "/operations", capability: "admin.operations.read"},
    {id: "cms", label: "Page builder", path: "/cms", capability: "admin.config.manage"},
    {id: "audit", label: "Audit trail", path: "/audit", capability: "admin.audit.read"},
  ],
  csrf_token: "csrf_synthetic_012345678901234567890123456789",
};
const operations = {
  changes: [{
    id: "change-synthetic-001", revision: 1, tenant_id: "tenant-synthetic-001", country: "IN",
    command: {domain: "WALLET", action: "ADJUST", target_id: "customer-synthetic-001", reason: "Correct verified settlement discrepancy", payload: {points: 100}, correlation_id: "corr-synthetic-operation-001"},
    risk: "HIGH", status: "PENDING_APPROVAL", requested_by: "admin-requester-001",
    created_at: "2026-08-27T06:40:00Z", updated_at: "2026-08-27T06:40:00Z",
  }],
};
const audit = {
  entries: [
    {id: "audit-synthetic-003", tenant_id: "tenant-synthetic-001", country: "IN", actor: {subject_id: "admin-synthetic-001", actor_type: "USER"}, action: "configuration.section.published", target: {type: "configuration_section", id: "home-hero"}, outcome: "SUCCEEDED", reason_code: "CONTENT_APPROVED", correlation_id: "corr-synthetic-admin-003", occurred_at: "2026-08-27T06:43:00Z", recorded_at: "2026-08-27T06:43:01Z", hash: "c".repeat(64), sequence: 3},
    {id: "audit-synthetic-002", tenant_id: "tenant-synthetic-001", country: "IN", actor: {subject_id: "support-synthetic-001", actor_type: "USER"}, action: "customer.profile.viewed", target: {type: "customer", id: "customer-synthetic-001"}, outcome: "DENIED", reason_code: "COUNTRY_SCOPE_MISMATCH", correlation_id: "corr-synthetic-admin-002", occurred_at: "2026-08-27T06:38:00Z", recorded_at: "2026-08-27T06:38:01Z", hash: "b".repeat(64), sequence: 2},
    {id: "audit-synthetic-001", tenant_id: "tenant-synthetic-001", country: "IN", actor: {subject_id: "admin-synthetic-001", actor_type: "USER"}, action: "admin.session.opened", target: {type: "session", id: "session-synthetic-001"}, outcome: "SUCCEEDED", reason_code: "SESSION_OPENED", correlation_id: "corr-synthetic-admin-001", occurred_at: "2026-08-27T06:30:00Z", recorded_at: "2026-08-27T06:30:01Z", hash: "a".repeat(64), sequence: 1},
  ],
  has_more: false,
};
const cmsPages = {
  items: [{
    tenant_id: "tenant-synthetic-001", country: "IN", revision: 3, updated_by: "admin-synthetic-001", updated_at: "2026-08-29T10:00:00Z",
    page: {id: "customer-home", route: "/app", title_key: "Customer home", audience: ["PUBLIC", "CUSTOMER"], enabled: true, blocks: [
      {id: "home-hero", kind: "HERO", title_key: "Welcome", enabled: true, priority: 0, content: {headline: "Smart shopping, everyday."}},
      {id: "home-products", kind: "ITEM_RAIL", title_key: "Bestsellers", enabled: true, priority: 1, content: {collection_id: "bestsellers", limit: 8}},
    ]},
  }],
};
const cmsWorkspace = {
  tenant_id: "tenant-synthetic-001", country: "IN", revision: 4, updated_by: "admin-synthetic-001", updated_at: "2026-08-29T10:00:00Z",
  workspace: {
    minimum_versions: {ANDROID: "1.0.0", IOS: "1.0.0"}, latest_versions: {ANDROID: "1.2.0", IOS: "1.2.0"},
    supported_locales: ["en", "ta"], default_locale: "en", consent_policies: [],
    flags: {shop: true, services: true, homes: true, classifieds: true, socio: true},
    home_sections: [{id: "hero", kind: "HERO", title_key: "Home hero", enabled: true, priority: 0}],
  },
};

createServer((request, response) => {
  response.setHeader("Content-Type", "application/json");
  response.setHeader("Cache-Control", "no-store");
  if (request.url?.startsWith("/admin/api/v1/session/country")) {
    response.statusCode = 204;
    response.end();
    return;
  }
  if (request.url?.startsWith("/admin/api/v1/session")) {
    response.end(JSON.stringify(session));
    return;
  }
  if (request.url?.startsWith("/admin/api/v1/audit/events")) {
    response.end(JSON.stringify(audit));
    return;
  }
  if (request.url === "/admin/api/v1/cms/pages") {
    response.end(JSON.stringify(cmsPages));
    return;
  }
  if (request.url === "/admin/api/v1/cms/workspace") {
    response.end(JSON.stringify(cmsWorkspace));
    return;
  }
  if (request.url?.startsWith("/admin/api/v1/operations")) {
    response.end(JSON.stringify(operations));
    return;
  }
  response.statusCode = 404;
  response.end(JSON.stringify({error: {code: "NOT_FOUND", message: "Not found", correlation_id: "corr-synthetic-404", retryable: false}}));
}).listen(4174, "127.0.0.1", () => process.stdout.write("Synthetic contract API listening on http://127.0.0.1:4174\n"));
