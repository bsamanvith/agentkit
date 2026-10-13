// The console's browser tests, against the real binary: agentkit serve with
// the agent and web components (make web-smoke builds it and sets
// AGENTKIT_BIN), and a fake model (e2e/fake-model.ts) in place of the API.
//
// Browsers are never downloaded (no `playwright install`): CI uses the
// runner's Google Chrome; locally, the Chromium Playwright finds in
// PLAYWRIGHT_BROWSERS_PATH or its cache.
import { defineConfig } from "@playwright/test";
import { modelPort, token } from "./e2e/token";

const port = 18478;
const bin = process.env.AGENTKIT_BIN;
if (!bin) throw new Error("set AGENTKIT_BIN to an agentkit binary (make web-smoke does)");

export default defineConfig({
  testDir: "e2e",
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    channel: process.env.CI ? "chrome" : undefined,
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: `node e2e/fake-model.ts ${modelPort}`,
      url: `http://127.0.0.1:${modelPort}/healthz`,
      reuseExistingServer: false,
      timeout: 10_000,
    },
    {
    command: `"${bin}" serve`,
    url: `http://127.0.0.1:${port}/readyz`,
    reuseExistingServer: false,
    timeout: 30_000,
    stdout: "pipe",
    env: {
      AGENTKIT_ADDR: `127.0.0.1:${port}`,
      AGENTKIT_COMPONENTS: "agent,web",
      AGENTKIT_CONFIG: "e2e/agentkit.json",
      AGENTKIT_API_TOKEN: token,
      ANTHROPIC_API_KEY: "smoke-dummy-key",
      ANTHROPIC_BASE_URL: `http://127.0.0.1:${modelPort}`,
    },
  },
  ],
});
