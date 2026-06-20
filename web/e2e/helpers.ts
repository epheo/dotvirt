import { expect, type Page } from '@playwright/test';

const TOKEN = process.env.OC_TOKEN ?? '';

// login authenticates with the OpenShift token and waits for the inventory shell.
export async function login(page: Page) {
	if (!TOKEN) throw new Error('OC_TOKEN env var is required for the e2e tests');
	await page.goto('/');
	await page.waitForSelector('textarea');
	await page.fill('textarea', TOKEN);
	await page.click('button[type="submit"]');
	// "New VM" in the header is unambiguous and appears once authenticated ("All VMs"
	// shows in both the tree and the breadcrumb, so it's not a unique anchor).
	await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
}

// openFirstVM switches to the VMs tab and opens the first VM's detail view.
export async function openFirstVM(page: Page) {
	await page.getByRole('button', { name: 'VMs', exact: true }).click();
	const row = page.locator('tbody tr').first();
	await expect(row).toBeVisible();
	await row.click();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
}

// ── GitOps round-trip helpers (roundtrip.spec.ts) ──────────────────────────────
// These mirror the knobs of hack/e2e-roundtrip.sh. Merging a PR is the human GitOps
// gate, so it uses a Forgejo bot token — there is deliberately no dotvirt UI affordance
// for it; every other step is driven through the app under the session cookie.
const FORGE = process.env.FORGE ?? 'https://forgejo.apps.hetznet.epheo.eu';
const FORGE_TOKEN = process.env.FORGE_TOKEN ?? '';
const FORGE_OWNER = process.env.OWNER ?? 'dotvirt';

// mergePR merges a Forgejo pull request, retrying while Forgejo finishes computing
// mergeability (a freshly opened PR reports non-200 until then).
export async function mergePR(page: Page, repo: string, pr: number) {
	for (let i = 0; i < 20; i++) {
		const res = await page.request.post(
			`${FORGE}/api/v1/repos/${FORGE_OWNER}/${repo}/pulls/${pr}/merge`,
			{ headers: { Authorization: `token ${FORGE_TOKEN}` }, data: { Do: 'merge' }, failOnStatusCode: false }
		);
		if (res.status() === 200) return;
		await page.waitForTimeout(2000);
	}
	throw new Error(`merge of PR #${pr} in ${repo} failed`);
}

// proposeAndMerge drives the Changes panel: it fills the PR title, clicks
// "Create pull request → <project>" (capturing the PR number from the propose response),
// then merges that PR and closes the panel. Assumes a single project is staged.
export async function proposeAndMerge(page: Page, project: string, title: string): Promise<number> {
	await page.getByRole('button', { name: /Changes/ }).click();
	await page.getByPlaceholder('Pull request title').fill(title);
	const [resp] = await Promise.all([
		page.waitForResponse((r) => r.url().includes('/api/draft/propose') && r.request().method() === 'POST'),
		page.getByRole('button', { name: /Create pull request/ }).click()
	]);
	const { prNumber } = (await resp.json()) as { prNumber?: number };
	if (!prNumber) throw new Error('propose returned no prNumber');
	await mergePR(page, project, prNumber);
	await page.locator('aside').getByRole('button', { name: 'Close' }).click(); // unobscure the VM table
	return prNumber;
}

// cleanupVM best-effort removes a leaked test VM via the API only (no UI), swallowing
// errors — the safety net when an assertion fails mid round-trip. A no-op once the VM is
// gone: the delete then stages nothing to propose.
export async function cleanupVM(page: Page, project: string, ns: string, vm: string) {
	try {
		const del = await page.request.post(`/api/vms/${ns}/${vm}/delete`, { failOnStatusCode: false });
		const draft = del.ok() ? ((await del.json().catch(() => null)) as { count?: number } | null) : null;
		if (!draft?.count) return;
		const resp = await page.request.post(`/api/draft/propose?project=${project}`, {
			data: { title: `e2e cleanup ${vm}`, message: '' },
			failOnStatusCode: false
		});
		if (!resp.ok()) return;
		const { prNumber } = (await resp.json()) as { prNumber?: number };
		if (prNumber) await mergePR(page, project, prNumber);
	} catch {
		/* best-effort */
	}
}
