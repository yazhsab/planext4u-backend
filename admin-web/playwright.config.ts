import { defineConfig, devices } from "@playwright/test";

const externalBaseURL = process.env.PLANEXT4U_E2E_BASE_URL;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: [["list"], ["html", {open: "never"}]],
  use: {
    baseURL: externalBaseURL ?? "http://127.0.0.1:4173",
    ignoreHTTPSErrors: externalBaseURL?.startsWith("https://") ?? false,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {name: "desktop-chromium", use: {...devices["Desktop Chrome"], viewport: {width: 1366, height: 768}}},
    {name: "mobile-chromium", use: {...devices["Pixel 7"]}},
  ],
  webServer: externalBaseURL ? undefined : {
    command: "npm run dev",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
