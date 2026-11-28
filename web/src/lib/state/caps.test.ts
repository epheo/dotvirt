import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
	api: { capabilities: vi.fn() },
	Unauthorized: class extends Error {},
}));
vi.mock('#lib/api.js', async (actual) => ({ ...(await actual<object>()), ...api }));

import { HttpError, READ_RETRY_MAX_MS, READ_RETRY_MS } from '#lib/api.js';
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

	it('asks again after a failure that may pass, backing off, but never after a refusal', async () => {
		vi.useFakeTimers();
		try {
			const gate = (ns: string) => caps.nsGate(ns, (n) => n.restart, 'x');
			api.api.capabilities.mockRejectedValue(new HttpError('unavailable', 503));
			gate('web');
			await vi.advanceTimersByTimeAsync(0);
			expect(api.api.capabilities).toHaveBeenCalledTimes(1);
			// A gate asked before the wait is up asks nothing.
			gate('web');
			await vi.advanceTimersByTimeAsync(0);
			expect(api.api.capabilities).toHaveBeenCalledTimes(1);
			await vi.advanceTimersByTimeAsync(READ_RETRY_MS * 1.3);
			gate('web');
			await vi.advanceTimersByTimeAsync(0);
			expect(api.api.capabilities).toHaveBeenCalledTimes(2);
			// The second wait is twice the first, at least 0.7 of 30s.
			await vi.advanceTimersByTimeAsync(READ_RETRY_MS * 1.3);
			gate('web');
			await vi.advanceTimersByTimeAsync(0);
			expect(api.api.capabilities).toHaveBeenCalledTimes(2);

			api.api.capabilities.mockReset();
			api.api.capabilities.mockRejectedValue(new HttpError('not found', 404));
			gate('db');
			await vi.advanceTimersByTimeAsync(READ_RETRY_MAX_MS * 3);
			gate('db');
			await vi.advanceTimersByTimeAsync(0);
			expect(api.api.capabilities).toHaveBeenCalledTimes(1);
		} finally {
			vi.useRealTimers();
		}
	});

	it('forgets every answer on reset', async () => {
		api.api.capabilities.mockResolvedValue({ cluster: { platform: true } });
		caps.clusterGate((c) => c.platform, 'x');
		await flush();
		caps.reset();
		expect(caps.clusterGate((c) => c.platform, 'x').why).toBe(CHECKING);
	});
});
