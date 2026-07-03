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

// This spec calls a dev/internal Forgejo directly (often a self-signed cert), so relax TLS
// here only — the smoke suite keeps verifying certs (see playwright.config.ts).
test.use({ ignoreHTTPSErrors: true });

test('create → Synced → delete → gone, observed in the inventory', async ({ page }) => {
	test.skip(
		!process.env.OC_TOKEN || !process.env.FORGE_TOKEN,
		'requires OC_TOKEN + FORGE_TOKEN against a live stack'
	);
	// Three serial sync waits (~SYNC_TIMEOUT each: row appears, flips to Synced, row gone)
	// plus two merge-retry loops and reloads — widen past a naive 4× so a slow ArgoCD can't
	// abort mid-assertion and skip the finally-cleanup.
	test.setTimeout(SYNC_TIMEOUT * 6);

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
		await expect(page.getByRole('button', { name: /^New$/ })).toBeVisible();
		await proposeAndMerge(page, PROJECT, `e2e: create ${vm}`);

		// The create lands in the rendered inventory: the actual VM row appears, then its
		// sync badge flips to Synced (Not tracked → Synced as ArgoCD's per-resource status
		// propagates through dotvirt's watch). Match the VMTable row by its Power: name —
		// the TaskDock's transient "Proposed"/"Configuration drift" overlay rows carry the
		// same VM name but a different prefix, so an unscoped `tbody tr` would match all three.
		await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
		const row = page.getByRole('row', { name: new RegExp(`Power:.*${vm}`) });
		await expect(row).toBeVisible({ timeout: SYNC_TIMEOUT });
		await expect(row.getByText('Synced')).toBeVisible({ timeout: SYNC_TIMEOUT });

		// Stage + merge the delete; the row disappears once ArgoCD prunes the VM.
		const deleted = await page.request.post(`/api/vms/${NS}/${vm}/delete`, { failOnStatusCode: false });
		expect(deleted.ok(), `stage delete → ${deleted.status()}`).toBeTruthy();

		await page.reload();
		await expect(page.getByRole('button', { name: /^New$/ })).toBeVisible();
		await proposeAndMerge(page, PROJECT, `e2e: delete ${vm}`);

		await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
		await expect(page.getByRole('row', { name: new RegExp(`Power:.*${vm}`) })).toHaveCount(0, { timeout: SYNC_TIMEOUT });
	} finally {
		await cleanupVM(page, PROJECT, NS, vm);
	}
});
