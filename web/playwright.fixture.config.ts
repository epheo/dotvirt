import { defineConfig, devices } from '@playwright/test';

// Hermetic UI suite: the built SPA against the fixture backend
// (e2e/fixture/server.mjs) — no cluster, no forge. Scenario state is global on
// the one server, so files run serially (workers: 1). `npm run build` first.
export default defineConfig({
	testDir: './e2e/ui',
	timeout: 30_000,
	expect: { timeout: 10_000 },
	fullyParallel: false,
	workers: 1,
	retries: process.env.CI ? 1 : 0,
	reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
	use: {
		baseURL: 'http://localhost:4173',
		trace: 'retain-on-failure',
	},
	webServer: {
		command: 'node e2e/fixture/server.mjs',
		url: 'http://localhost:4173/api/healthz',
		reuseExistingServer: !process.env.CI,
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
