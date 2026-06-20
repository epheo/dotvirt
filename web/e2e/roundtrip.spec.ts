import { expect, test } from '@playwright/test';
import { cleanupVM, login, proposeAndMerge } from './helpers';

// Full GitOps round-trip, observed in the browser: stage a throwaway VM, propose + merge
// its PR through the app, and watch the inventory reflect create → Synced, then
// delete → gone. This is the deterministic propagation the event-bus refactor delivers —
// instant once the on-push webhook nudge is healthy, otherwise bounded by ArgoCD's poll.
// It complements the timing harness (hack/e2e-roundtrip.sh) by proving the *rendered UI*
// updates, not just the API.
//
// Opt-in: needs a live stack plus a Forgejo bot token to merge (the human GitOps gate).
// Skipped unless OC_TOKEN and FORGE_TOKEN are set, so the default `npm run test:e2e`
// smoke run is unaffected.

const PROJECT = process.env.PROJECT ?? 'team-a';
const NS = process.env.NS ?? PROJECT;
const SYNC_TIMEOUT = Number(process.env.SYNC_TIMEOUT ?? 240_000); // ArgoCD-sync ceiling, per the bash harness

test('create → Synced → delete → gone, observed in the inventory', async ({ page }) => {
	test.skip(
		!process.env.OC_TOKEN || !process.env.FORGE_TOKEN,
		'requires OC_TOKEN + FORGE_TOKEN against a live stack'
	);
	test.setTimeout(SYNC_TIMEOUT * 4); // two sync waits plus merge retries

	const vm = `e2e-rt-${Date.now()}`;
	await login(page);

	try {
		// Stage the create via the same endpoint the New-VM wizard posts to — the 6-step
		// wizard depends on live cluster options and is brittle to drive, and this spec's
		// value is the browser-observed propagation, not the modal. page.request shares the
		// session cookie, so the staged change appears in the Changes panel after a reload.
		const created = await page.request.post('/api/vms', {
			data: {
				name: vm,
				namespace: NS,
				instancetype: 'u1.medium',
				preference: 'fedora',
				osImage: { name: 'fedora', namespace: 'openshift-virtualization-os-images' },
				diskSize: '10Gi',
				running: false
			},
			failOnStatusCode: false
		});
		expect(created.ok(), `stage create → ${created.status()}`).toBeTruthy();

		await page.reload();
		await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
		await proposeAndMerge(page, PROJECT, `e2e: create ${vm}`);

		// The create lands in the rendered inventory: the row appears, then flips to Synced.
		await page.getByRole('button', { name: 'VMs', exact: true }).click();
		const row = page.locator('tbody tr', { hasText: vm });
		await expect(row).toBeVisible({ timeout: SYNC_TIMEOUT });
		await expect(row.getByText('Synced')).toBeVisible({ timeout: SYNC_TIMEOUT });

		// Stage + merge the delete; the row disappears once ArgoCD prunes the VM.
		const deleted = await page.request.post(`/api/vms/${NS}/${vm}/delete`, { failOnStatusCode: false });
		expect(deleted.ok(), `stage delete → ${deleted.status()}`).toBeTruthy();

		await page.reload();
		await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
		await proposeAndMerge(page, PROJECT, `e2e: delete ${vm}`);

		await page.getByRole('button', { name: 'VMs', exact: true }).click();
		await expect(page.locator('tbody tr', { hasText: vm })).toHaveCount(0, { timeout: SYNC_TIMEOUT });
	} finally {
		await cleanupVM(page, PROJECT, NS, vm);
	}
});
