import { defineConfig } from "@playwright/test";

// Real-browser display-conformance tests. Kept in ./e2e (out of vitest's
// src/**/*.test.ts scope) and named *.e2e.test.ts so the shared eslint config
// applies its relaxed test-file rules. Run with `npm run test:e2e`
// (needs `npx playwright install chromium` once). The tier-3 test is a
// mechanical getComputedStyle dump over CDP (no screenshots), so no pixel-diff
// config is needed.
export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.e2e.test.ts",
  fullyParallel: false,
  forbidOnly: !!process.env["CI"],
  reporter: [["list"]],
  projects: [
    {
      name: "chromium",
      use: {
        browserName: "chromium",
        headless: true,
        viewport: { width: 800, height: 600 },
      },
    },
  ],
});
