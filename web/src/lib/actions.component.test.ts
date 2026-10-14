import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Inventory, VM } from './api';
import { adoptNamespaces, dispatchVMAction, manifestURL, vmActions } from './actions';
import { inventory } from './state/inventory.svelte';
import { ui } from './state/ui.svelte';
import { navigations } from '../test/app-navigation';

// The registry is the one place a VM menu's labels, gates and dispatch live;
// every menu renders a projection of it, so its gates are the menus' gates.

const vm = (over: Partial<VM> = {}): VM => ({
	namespace: 'team-a',
	name: 'web',
	power: 'On',
	sync: 'Synced',
	sourceFile: 'vms/web.yaml',
	phase: 'Running',
	...over,
});

const action = (id: string) => vmActions.find((a) => a.id === id)!;
const enabledIds = (v: VM) => vmActions.filter((a) => a.enabled(v)).map((a) => a.id);

let fetchMock: ReturnType<typeof vi.fn>;

// The staging verbs also need the caller's edit right in the VM's namespace.
const grant = (canAuthor: boolean) =>
	inventory.apply({
		projects: [
			{
				name: 'p',
				repo: 'https://forge/p.git',
				namespaces: [{ namespace: 'team-a', vms: [], canAuthor }],
			},
		],
	} as unknown as Inventory);

beforeEach(() => {
	ui.reset();
	grant(true);
	navigations.length = 0;
	fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
	vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('vmActions gates', () => {
	it('a viewer gets no staging verbs', () => {
		grant(false);
		const ids = enabledIds(vm({ disks: [{ name: 'root', type: 'dataVolume' }] } as Partial<VM>));
		for (const id of ['edit', 'delete', 'template', 'migrate-storage'])
			expect(ids).not.toContain(id);
		expect(enabledIds(vm({ sync: 'NotTracked', sourceFile: undefined }))).not.toContain('adopt');
		expect(ids).toContain('restart');
	});

	it('ids are unique', () => {
		const ids = vmActions.map((a) => a.id);
		expect(new Set(ids).size).toBe(ids.length);
	});

	it('a running git-declared VM offers everything but unpause and adopt', () => {
		expect(enabledIds(vm())).toEqual([
			'restart',
			'pause',
			'migrate',
			'console',
			'snapshot',
			'clone',
			'template',
			'edit',
			'manifest',
			'delete',
		]);
	});

	it('a paused VM can be unpaused, not paused again', () => {
		const ids = enabledIds(vm({ paused: true }));
		expect(ids).toContain('unpause');
		expect(ids).not.toContain('pause');
	});

	it('a stopped VM offers no runtime verbs or console', () => {
		const ids = enabledIds(vm({ phase: undefined, power: 'Off' }));
		for (const id of ['restart', 'pause', 'unpause', 'migrate', 'migrate-storage', 'console']) {
			expect(ids).not.toContain(id);
		}
		expect(ids).toContain('edit');
	});

	it('a cluster-only VM can only be adopted, never edited through git', () => {
		const ids = enabledIds(vm({ sourceFile: '', sync: 'NotTracked' }));
		expect(ids).toContain('adopt');
		for (const id of ['edit', 'template', 'manifest', 'delete', 'migrate-storage']) {
			expect(ids).not.toContain(id);
		}
	});

	it('storage migration needs a DataVolume disk to move', () => {
		expect(action('migrate-storage').enabled(vm())).toBe(false);
		const withDV = vm({
			disks: [{ name: 'root', type: 'dataVolume' } as NonNullable<VM['disks']>[0]],
		});
		expect(action('migrate-storage').enabled(withDV)).toBe(true);
	});

	it('every runtime action runs, every dialog action opens a modal', () => {
		for (const a of vmActions) {
			if (a.kind === 'runtime') expect(a.run, a.id).toBeTypeOf('function');
			if (a.open) expect(a.open(vm()).kind, a.id).toBeTruthy();
		}
	});
});

describe('dispatchVMAction', () => {
	it('runs a runtime verb under the API and toasts success', async () => {
		await dispatchVMAction(action('restart'), vm());
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/vms/team-a/web/restart',
			expect.objectContaining({ method: 'POST' }),
		);
		expect(ui.toasts.at(-1)).toMatchObject({ kind: 'success', msg: 'Restart requested for web.' });
	});

	it('toasts the server message on failure', async () => {
		fetchMock.mockResolvedValueOnce(new Response('VMI is not running', { status: 409 }));
		await dispatchVMAction(action('pause'), vm());
		const t = ui.toasts.at(-1)!;
		expect(t.kind).toBe('error');
		expect(t.msg).toContain('Pause failed for web');
		expect(t.msg).toContain('VMI is not running');
	});

	it('stays silent on a 401: sign-out is central', async () => {
		fetchMock.mockResolvedValueOnce(new Response('', { status: 401 }));
		await dispatchVMAction(action('unpause'), vm({ paused: true }));
		expect(ui.toasts).toEqual([]);
	});

	it('opens a dialog action as a modal on the VM page', async () => {
		await dispatchVMAction(action('edit'), vm());
		expect(ui.modal).toEqual({ kind: 'editVM', vm: { namespace: 'team-a', name: 'web' } });
		expect(navigations).toEqual(['/vm/team-a/web']);
	});

	it('routes tab actions to their tab', async () => {
		await dispatchVMAction(action('console'), vm());
		await dispatchVMAction(action('snapshot'), vm());
		expect(navigations).toEqual(['/vm/team-a/web?tab=console', '/vm/team-a/web?tab=snapshots']);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('downloads the manifest from git', async () => {
		const open = vi.fn();
		vi.stubGlobal('open', open);
		await dispatchVMAction(action('manifest'), vm());
		expect(open).toHaveBeenCalledWith(manifestURL(vm()), '_blank');
		expect(manifestURL(vm())).toBe('/api/vms/team-a/web/manifest');
	});

	it('adopts a cluster-only VM into the draft', async () => {
		fetchMock.mockResolvedValueOnce(
			Response.json({ base: 'main', branch: '', count: 1, items: [] }),
		);
		await dispatchVMAction(action('adopt'), vm({ sourceFile: '', sync: 'NotTracked' }));
		expect(fetchMock.mock.calls[0][0]).toBe('/api/vms/team-a/web/adopt');
		expect(ui.toasts.at(-1)).toMatchObject({ kind: 'success' });
		expect(ui.toasts.at(-1)!.action?.label).toBe('Review & propose');
	});
});

describe('adoptNamespaces', () => {
	it('keeps going past a namespace that fails and says what was left', async () => {
		fetchMock
			.mockResolvedValueOnce(new Response('nothing to adopt', { status: 404 }))
			.mockResolvedValueOnce(Response.json({ base: 'main', count: 2, items: [] }));
		await adoptNamespaces(['ns-empty', 'ns-full']);
		const t = ui.toasts.at(-1)!;
		expect(t.kind).toBe('warning');
		expect(t.msg).toContain('ns-empty');
	});

	it('reports an error when nothing staged', async () => {
		fetchMock.mockResolvedValue(new Response('forbidden', { status: 403 }));
		await adoptNamespaces(['ns-a']);
		expect(ui.toasts.at(-1)).toMatchObject({ kind: 'error' });
	});
});
