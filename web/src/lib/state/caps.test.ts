import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
	api: { capabilities: vi.fn() },
	Unauthorized: class extends Error {},
}));
vi.mock('#lib/api.js', () => api);

import { caps, CHECKING } from './caps.svelte';
import { ACCESS_UNKNOWN } from '#lib/authoring.js';

const flush = () => new Promise((r) => setTimeout(r, 0));
const own = { create: true, update: true, delete: false };

afterEach(() => {
	caps.reset();
	api.api.capabilities.mockReset();
});

describe('caps', () => {
	it('closes while checking, then offers what the answer allows', async () => {
		api.api.capabilities.mockResolvedValue({ cluster: { platform: true, uplink: own } });
		expect(caps.clusterGate((c) => c.platform, 'You may not')).toEqual({
			ok: false,
			why: CHECKING,
		});
		await flush();
		expect(caps.clusterGate((c) => c.platform, 'no').ok).toBe(true);
		expect(caps.clusterGate((c) => c.uplink.delete, 'You may not delete uplinks.')).toEqual({
			ok: false,
			why: 'You may not delete uplinks.',
		});
	});

	it('reads a missing field as no', async () => {
		api.api.capabilities.mockResolvedValue({ cluster: {} });
		expect(caps.cluster).toBeNull();
		await flush();
		expect(caps.clusterGate((c) => c.drsDisable, 'You may not turn off rebalancing.').ok).toBe(
			false,
		);
		expect(caps.clusterGate((c) => c.uplink.update, 'no').ok).toBe(false);
	});

	it('says the check failed, not a denial, when the read fails', async () => {
		api.api.capabilities.mockRejectedValue(new Error('503'));
		caps.nsGate('web', (n) => n.restart, 'You may not restart VMs here.');
		await flush();
		expect(caps.nsGate('web', (n) => n.restart, 'x')).toEqual({ ok: false, why: ACCESS_UNKNOWN });
	});

	it('reads a namespace once, and again after an invalidate', async () => {
		api.api.capabilities.mockResolvedValue({ namespace: { namespace: 'web', restart: true } });
		caps.ns('web');
		caps.ns('web');
		await flush();
		expect(caps.nsGate('web', (n) => n.restart, 'x').ok).toBe(true);
		expect(api.api.capabilities).toHaveBeenCalledTimes(1);
		expect(api.api.capabilities).toHaveBeenCalledWith('web');
		api.api.capabilities.mockResolvedValue({ namespace: { namespace: 'web', restart: false } });
		caps.invalidate();
		// The answer it replaces holds until the new one lands: no flicker.
		expect(caps.nsGate('web', (n) => n.restart, 'x').ok).toBe(true);
		await flush();
		expect(api.api.capabilities).toHaveBeenCalledTimes(2);
		expect(caps.nsGate('web', (n) => n.restart, 'no')).toEqual({ ok: false, why: 'no' });
	});

	it('forgets every answer on reset', async () => {
		api.api.capabilities.mockResolvedValue({ cluster: { platform: true } });
		caps.clusterGate((c) => c.platform, 'x');
		await flush();
		caps.reset();
		expect(caps.clusterGate((c) => c.platform, 'x').why).toBe(CHECKING);
	});
});
