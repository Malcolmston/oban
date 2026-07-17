import { defineConfig, devices } from '@playwright/test';

// Generate one Playwright PROJECT per built-in device descriptor. Playwright
// ships 100+ device profiles; every project is forced onto the chromium engine
// so the whole matrix runs where only chromium is installed. This is the
// per-device responsive sweep of the site.
const deviceNames = Object.keys(devices);
const projects = deviceNames.map((name) => ({
  name,
  use: {
    ...devices[name],
    browserName: 'chromium' as const,
    defaultBrowserType: 'chromium' as const,
  },
}));

// Guard: the "100+ device profiles" guarantee must never silently regress.
if (projects.length < 100) {
  throw new Error(`Expected >= 100 device projects, got ${projects.length}`);
}

const BASE_URL = 'http://localhost:4173/oban/';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  workers: 24,
  // Generous per-test timeout: the link/nav sweeps navigate every page, and the
  // 100+ project matrix shares a single preview server under 24 workers.
  timeout: 60_000,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],
  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
  },
  projects,
  webServer: {
    // Generate the API doc.json (consumed by the React Docs tab) into
    // web/public before building so Vite bundles it into dist/doc.json, then
    // build and serve the production preview.
    command:
      'cd .. && go run ./docs/gen -json web/public/doc.json && cd web && npm run build && npm run preview -- --port 4173 --strictPort',
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
