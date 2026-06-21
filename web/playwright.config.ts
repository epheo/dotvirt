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
		// TLS is verified by default so the smoke suite catches a genuinely broken-cert
		// deployment. Set E2E_INSECURE_TLS=1 to tolerate a self-signed cert when running
		// against a live dev/eval stack (an OpenShift ingress cert isn't in Playwright's
		// trust store). roundtrip.spec.ts relaxes it unconditionally (test.use), since it
		// also calls a dev Forgejo cross-origin.
		ignoreHTTPSErrors: !!process.env.E2E_INSECURE_TLS,
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});
