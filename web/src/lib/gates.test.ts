import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
	api: { capabilities: vi.fn() },
	Unauthorized: class extends Error {},
}));
vi.mock('#lib/api.js', () => api);

import { ACCESS_UNKNOWN, NOT_IN_GIT } from './authoring';
import { clusterGate, objectGate, repoGate, templateSaveGate, vmEditGate, vmGate } from './gates';
import { caps } from './state/caps.svelte';

// Every control reads the field its server gate asks, and says why not in one
// line: git does not hold the VM, the check failed, or the server's no.

const flush = () => new Promise((r) => setTimeout(r, 0));
const own = (v: boolean) => ({ create: v, update: v, delete: v });

afterEach(() => {
	caps.reset();
	api.api.capabilities.mockReset();
});

async function answer(body: unknown) {
	api.api.capabilities.mockResolvedValue(body);
	caps.invalidate();
	void caps.cluster;
	caps.ns('web');
	await flush();
}

describe('vmEditGate', () => {
	it('names git first, then the right to change VMs there', async () => {
		await answer({ namespace: { namespace: 'web', vm: own(false) } });
		expect(vmEditGate({ namespace: 'web', sourceFile: '' }).why).toBe(NOT_IN_GIT);
		expect(vmEditGate({ namespace: 'web', sourceFile: 'vms/a.yaml' }).why).toBe(
			'You may not change VMs in web.',
		);
		await answer({ namespace: { namespace: 'web', vm: own(true) } });
		expect(vmEditGate({ namespace: 'web', sourceFile: 'vms/a.yaml' }).ok).toBe(true);
	});

	it('says the check failed when it did, not a denial', async () => {
		api.api.capabilities.mockRejectedValue(new Error('down'));
		caps.invalidate();
		vmGate.update('web');
		await flush();
		expect(vmGate.update('web').why).toBe(ACCESS_UNKNOWN);
	});
});

describe('objectGate', () => {
	it('reads the field of the object kind, cluster or namespaced', async () => {
		await answer({
			cluster: { uplink: { create: true, update: false, delete: true } },
			namespace: { namespace: 'web', networkPolicy: own(true) },
		});
		expect(objectGate({ resource: 'uplink', namespace: 'cluster' }, 'create').ok).toBe(true);
		expect(objectGate({ resource: 'uplink', namespace: 'cluster' }, 'update').why).toBe(
			'You may not change uplinks.',
		);
		expect(objectGate({ resource: 'networkpolicy', namespace: 'web' }, 'delete').ok).toBe(true);
		// An answer without the kind is a no.
		expect(objectGate({ resource: 'egressfirewall', namespace: 'web' }, 'create').ok).toBe(false);
	});
});

describe('the fields the audit named', () => {
	it('baseline cluster rules read their own field, not the admin one', async () => {
		await answer({
			cluster: { adminNetworkPolicy: own(true), baselineAdminNetworkPolicy: own(false) },
		});
		const baseline = { resource: 'baselineadminnetworkpolicy', namespace: 'cluster' };
		expect(objectGate(baseline, 'update').why).toBe(
			'You may not change the baseline cluster rule.',
		);
		expect(objectGate({ ...baseline, resource: 'adminnetworkpolicy' }, 'update').ok).toBe(true);
	});

	it('turning rebalancing off is its own field', async () => {
		await answer({ cluster: { drsEnable: true, drsDisable: false } });
		expect(clusterGate.rebalanceOn().ok).toBe(true);
		expect(clusterGate.rebalanceOff().why).toBe('You may not turn off rebalancing.');
	});

	it('sync access reads the cluster grant, whatever the project says', async () => {
		await answer({
			cluster: { platform: true, grantAdmin: false },
			namespace: { namespace: 'web', project: { name: 'p', grantAdmin: true } },
		});
		expect(clusterGate.syncAccess().why).toBe('You may not grant sync access.');
		await answer({ cluster: { platform: true, grantAdmin: true } });
		expect(clusterGate.syncAccess().ok).toBe(true);
	});
});

describe('repoGate', () => {
	it('attaches on the project field, recovers on namespace create', async () => {
		await answer({
			cluster: { namespaceCreate: false },
			namespace: { namespace: 'web', project: { name: 'p', adopt: true } },
		});
		const nss = [{ namespace: 'web' }];
		expect(repoGate({ namespaces: nss }).ok).toBe(true);
		expect(repoGate({ repo: 'https://forge/p.git', namespaces: nss }).why).toBe(
			'You may not create projects or namespaces.',
		);
	});
});

describe('templateSaveGate', () => {
	it('opens on either library', async () => {
		await answer({
			cluster: { sharedLibrary: true },
			namespace: { namespace: 'web', project: { name: 'p', templateEdit: false } },
		});
		expect(templateSaveGate('web').ok).toBe(true);
		await answer({
			cluster: { sharedLibrary: false },
			namespace: { namespace: 'web', project: { name: 'p', templateEdit: false } },
		});
		expect(templateSaveGate('web').why).toBe("You may not change this project's templates.");
	});
});
