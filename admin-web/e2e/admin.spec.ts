import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

import { adminSession, auditPage } from "../src/test/fixtures";

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
