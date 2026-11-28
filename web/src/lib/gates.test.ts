import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
	api: { capabilities: vi.fn(), desktopCapabilities: vi.fn() },
	Unauthorized: class extends Error {},
}));
vi.mock('#lib/api.js', () => api);

import { ACCESS_UNKNOWN, NOT_IN_GIT } from './authoring';
import {
	clusterGate,
	desktopGate,
	desktopsGate,
	hostGate,
	imageGate,
	namespaceHistoryGate,
	objectGate,
	projectGate,
	repoGate,
	repoReadGate,
	templateSaveGate,
	vmEditGate,
	vmGate,
} from './gates';
import { caps } from './state/caps.svelte';

// Every control reads the field its server gate asks, and says why not in one
// line: git does not hold the VM, the check failed, or the server's no.

const flush = () => new Promise((r) => setTimeout(r, 0));
const own = (v: boolean) => ({ create: v, update: v, delete: v });

afterEach(() => {
	caps.reset();
	api.api.capabilities.mockReset();
	api.api.desktopCapabilities.mockReset();
});

async function answer(body: unknown) {
	api.api.capabilities.mockResolvedValue(body);
	caps.invalidate();
	void caps.cluster;
	caps.ns('web');
	await flush();
}

describe('imageGate', () => {
	it('offers deleting a boot image on its own field', async () => {
		await answer({
			namespace: { namespace: 'web', bootImageImport: true, bootImageDelete: false },
		});
		expect(imageGate.import('web').ok).toBe(true);
		expect(imageGate.delete('web').why).toBe('You may not delete boot images in web.');
		await answer({ namespace: { namespace: 'web', bootImageDelete: true } });
		expect(imageGate.delete('web').ok).toBe(true);
	});
});

describe('desktopsGate', () => {
	it('offers turning desktops on in the namespaces its project answer lists', async () => {
		await answer({
			namespace: { namespace: 'web', project: { name: 'shop', desktops: ['web'] } },
		});
		expect(desktopsGate.on('web', 'web').ok).toBe(true);
		expect(desktopsGate.on('web', 'db').why).toBe('You may not turn desktops on in db.');
	});
});

describe('repoReadGate', () => {
	it("reads a tenant project's whole repo apart from one namespace's history", async () => {
		await answer({
			cluster: { platformRead: false },
			namespace: { namespace: 'web', repoRead: true, project: { name: 'team', repoRead: false } },
		});
		expect(namespaceHistoryGate('web').ok).toBe(true);
		expect(repoReadGate('team', 'web').why).toBe('You may not read all of team.');
		expect(repoReadGate('platform', 'web').why).toBe("You may not read the platform's changes.");
		await answer({
			cluster: { platformRead: true },
			namespace: { namespace: 'web', repoRead: false, project: { name: 'team', repoRead: true } },
		});
		expect(namespaceHistoryGate('web').why).toBe('You may not read the history in web.');
		expect(repoReadGate('team', 'web').ok).toBe(true);
		expect(repoReadGate('platform', '').ok).toBe(true);
	});
});

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

	it('sync access reads the project field', async () => {
		await answer({
			cluster: { platform: true, grantAdmin: true },
			namespace: { namespace: 'web', project: { name: 'p', syncAccess: false } },
		});
		expect(projectGate.syncAccess('web').why).toBe('You may not grant sync access.');
		await answer({ namespace: { namespace: 'web', project: { name: 'p', syncAccess: true } } });
		expect(projectGate.syncAccess('web').ok).toBe(true);
	});
});

describe('repoGate', () => {
	it('attaches and recovers on the project adopt field', async () => {
		await answer({
			cluster: { namespaceCreate: false },
			namespace: { namespace: 'web', project: { name: 'p', adopt: true } },
		});
		const nss = [{ namespace: 'web' }];
		expect(repoGate({ namespaces: nss }).ok).toBe(true);
		expect(repoGate({ repo: 'https://forge/p.git', namespaces: nss }).ok).toBe(true);
		await answer({ namespace: { namespace: 'web', project: { name: 'p', adopt: false } } });
		expect(repoGate({ repo: 'https://forge/p.git', namespaces: nss }).why).toBe(
			"You may not recover this project's repo.",
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

describe('desktopGate', () => {
	const bobs = {
		namespace: 'web',
		name: 'eng-1',
		pool: 'eng',
		claim: { name: 'eng-bob' },
		own: false,
	};

	it('reads one answer for a desktop and its claim, by name, for its three acts', async () => {
		api.api.desktopCapabilities.mockResolvedValue({
			desktops: {
				namespace: 'web',
				desktop: 'eng-1',
				claim: 'eng-bob',
				restart: true,
				power: false,
				release: true,
			},
		});
		desktopGate.restart(bobs);
		desktopGate.power(bobs);
		desktopGate.release(bobs);
		await flush();
		expect(api.api.desktopCapabilities).toHaveBeenCalledTimes(1);
		expect(api.api.desktopCapabilities).toHaveBeenCalledWith('web', 'eng-1', 'eng-bob');
		expect(desktopGate.restart(bobs).ok).toBe(true);
		expect(desktopGate.power(bobs).why).toBe(
			"You may not start or stop others' desktops of pool eng.",
		);
		expect(desktopGate.power({ ...bobs, own: true }).why).toBe(
			'You may not start or stop your desktop of pool eng.',
		);
		expect(desktopGate.release(bobs).ok).toBe(true);
		expect(api.api.capabilities).not.toHaveBeenCalled();
	});

	it('asks each desktop, and each claim of it, apart', async () => {
		api.api.desktopCapabilities.mockImplementation(
			(namespace: string, desktop: string, claim: string) =>
				Promise.resolve({ desktops: { namespace, desktop, claim, restart: desktop === 'solo' } }),
		);
		// Its owner's claim is no claim of a pool: the answer is asked without it.
		const solo = { namespace: 'web', name: 'solo', pool: '', claim: { name: 'solo' }, own: true };
		desktopGate.restart(solo);
		desktopGate.restart(bobs);
		desktopGate.restart({ ...bobs, claim: { name: 'eng-carol' } });
		await flush();
		expect(api.api.desktopCapabilities).toHaveBeenCalledTimes(3);
		expect(api.api.desktopCapabilities).toHaveBeenCalledWith('web', 'solo', '');
		expect(desktopGate.restart(solo).ok).toBe(true);
		expect(desktopGate.restart(bobs).why).toBe('You may not restart eng-1.');
	});
});

describe('hostGate', () => {
	it("reads the host read's own answer", () => {
		expect(hostGate({ canCordon: true })).toEqual({ ok: true, why: '' });
		expect(hostGate({ canCordon: false })).toEqual({
			ok: false,
			why: 'You may not cordon or drain hosts.',
		});
	});
});
