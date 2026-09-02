import { defineConfig, devices } from '@playwright/test';

// Scratch config (not committed): the live suite with the hetznet hosts pinned
// in Chromium's resolver - the public DNS records for api./apps. are missing.
export default defineConfig({
	testDir: './e2e',
	timeout: 30_000,
	expect: { timeout: 10_000 },
	fullyParallel: false,
	reporter: [['list']],
	use: {
		baseURL: process.env.BASE_URL,
		ignoreHTTPSErrors: true,
		trace: 'retain-on-failure',
		launchOptions: { args: ['--host-resolver-rules=MAP *.hetznet.epheo.eu 5.9.142.227'] },
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
