import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

import { adminSession, auditPage, cmsPageDraftPage, cmsWorkspaceDraft, governanceView, operationPage, reportDetail, reportExport, reportPage } from "../src/test/fixtures";

test.beforeEach(async ({page}) => {
  await page.route("**/admin/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/admin/api/v1/session") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(adminSession)});
      return;
    }
    if (path === "/admin/api/v1/audit/events") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(auditPage)});
      return;
    }
    if (path === "/admin/api/v1/operations") {
      const body = route.request().method() === "POST"
        ? {...operationPage.changes[0], id: "change-report-export-001", command: {...operationPage.changes[0]?.command, domain: "REPORTING", action: "EXPORT", target_id: "social-active"}, risk: "STANDARD", status: "EXECUTED"}
        : operationPage;
      await route.fulfill({status: route.request().method() === "POST" ? 201 : 200, contentType: "application/json", body: JSON.stringify(body)});
      return;
    }
    if (path === "/admin/api/v1/governance") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(governanceView)});
      return;
    }
    if (path === "/admin/api/v1/reports") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(reportPage)});
      return;
    }
    if (path === "/admin/api/v1/reports/social-active/exports") {
      await route.fulfill({status: 202, contentType: "application/json", body: JSON.stringify(reportExport)});
      return;
    }
    if (path === "/admin/api/v1/reports/social-active") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(reportDetail)});
      return;
    }
    if (path === `/admin/api/v1/report-exports/${reportExport.id}`) {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(reportExport)});
      return;
    }
    if (path === "/admin/api/v1/cms/pages") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(cmsPageDraftPage)});
      return;
    }
    if (path === "/admin/api/v1/cms/workspace") {
      await route.fulfill({status: 200, contentType: "application/json", body: JSON.stringify(cmsWorkspaceDraft)});
      return;
    }
    if (path === "/admin/api/v1/session/country") {
      await route.fulfill({status: 204});
      return;
    }
    await route.fulfill({status: 404});
  });
});

test("authorized administrator can review the audit trail", async ({page}) => {
  await page.goto("/audit");
  await expect(page.getByRole("heading", {name: "Audit trail"})).toBeVisible();
  await expect(page.getByText("admin.session.opened")).toBeVisible();
  await expect(page.getByText("corr-synthetic-admin-001")).toBeVisible();

  const results = await new AxeBuilder({page}).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("authorized administrator can review controlled operations", async ({page}) => {
  await page.goto("/operations");
  await expect(page.getByRole("heading", {name: "Privileged operations"})).toBeVisible();
  await expect(page.getByText("customer-synthetic-001")).toBeVisible();
  await expect(page.getByText("Pending Approval")).toBeVisible();

  const results = await new AxeBuilder({page}).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("authorized administrator can review masked reports and request an audited export", async ({page}) => {
  await page.goto("/reports");
  await expect(page.getByRole("heading", {name: "Country reports"})).toBeVisible();
  await expect(page.getByText("Socio active")).toBeVisible();
  await page.getByRole("button", {name: "Request CSV export for Socio active"}).click();
  await page.getByLabel("Verified export reason").fill("Quarterly operations reconciliation");
  await page.getByRole("button", {name: "Submit request"}).click();
  await expect(page.getByRole("link", {name: "Download CSV"})).toBeVisible();
  await page.getByRole("link", {name: "Socio active"}).click();
  await expect(page.getByRole("heading", {name: "Socio active"})).toBeVisible();
  await expect(page.getByText("governance.report_cards")).toBeVisible();

  const results = await new AxeBuilder({page}).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("authorized content administrator can configure page drafts and workspace flags", async ({page}) => {
  await page.goto("/cms");
  await expect(page.getByRole("heading", {name: "CMS page builder"})).toBeVisible();
  await expect(page.getByLabel("Edit Customer home")).toBeVisible();
  await expect(page.getByText("home-hero")).toBeVisible();
  await page.getByRole("tab", {name: "App workspace"}).click();
  await expect(page.getByRole("heading", {name: "Global app workspace"})).toBeVisible();
  await expect(page.getByText("services")).toBeVisible();

  const results = await new AxeBuilder({page}).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("keyboard users can skip directly to the workspace", async ({page}) => {
  await page.goto("/");
	await expect(page.getByRole("heading", {name: "Administration overview"})).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", {name: "Skip to main content"})).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main-content")).toBeFocused();
});

test("mobile navigation opens without obscuring country context", async ({page}, testInfo) => {
  test.skip(!testInfo.project.name.startsWith("mobile"), "Mobile-only navigation behavior");
  await page.goto("/");
  const menuButton = page.getByRole("button", {name: "Open navigation"});
  await expect(menuButton).toBeVisible();
  await menuButton.click();
  await expect(page.getByRole("navigation", {name: "Primary navigation"})).toBeVisible();
  await expect(page.getByRole("combobox", {name: "Country context"})).toBeVisible();
});
