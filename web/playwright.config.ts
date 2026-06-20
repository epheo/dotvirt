import { defineConfig, devices } from '@playwright/test';

// e2e runs against the already-running dev stack (vite on :5173 + the dotvirt
// backend on :8080 against a live cluster). It needs OC_TOKEN in the environment.
export default defineConfig({
	testDir: './e2e',
	timeout: 30_000,
	expect: { timeout: 10_000 },
	fullyParallel: false,
	retries: 0,
	reporter: [['list']],
	use: {
		baseURL: process.env.BASE_URL ?? 'http://localhost:5173',
		// e2e runs against dev/internal stacks (the round-trip spec also calls Forgejo
		// directly to merge); tolerate their certs, as the bash harness does with curl -k.
		ignoreHTTPSErrors: true,
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});
