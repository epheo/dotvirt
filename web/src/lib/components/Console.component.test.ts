import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { VM } from '#lib/api.js';
import Console from './Console.svelte';

// The console's way back depends on why it dropped: a session the server
// ended (close 1008) is refused on every redial, so it offers sign-in; any
// other drop offers a reconnect.

const viewers = vi.hoisted(() => [] as { socket: EventTarget; events: EventTarget }[]);
vi.mock('@novnc/novnc', () => ({
	default: class {
		events = new EventTarget();
		scaleViewport = false;
		background = '';
		constructor(_target: HTMLElement, socket: EventTarget) {
			viewers.push({ socket, events: this.events });
		}
		addEventListener(type: string, fn: EventListener) {
			this.events.addEventListener(type, fn);
		}
		disconnect() {}
	},
}));

class FakeSocket extends EventTarget {
	constructor(readonly url: string) {
		super();
	}
}

const vm = { namespace: 'team-a', name: 'web', phase: 'Running' } as VM;

async function drop(code: number, clean = false) {
	render(Console, { vm });
	await vi.waitFor(() => expect(viewers).toHaveLength(1));
	const { socket, events } = viewers[0];
	socket.dispatchEvent(Object.assign(new Event('close'), { code }));
	events.dispatchEvent(new CustomEvent('disconnect', { detail: { clean } }));
}

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	viewers.length = 0;
	vi.stubGlobal('WebSocket', FakeSocket);
	fetchMock = vi.fn(async () => new Response('', { status: 401 }));
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

describe('Console', () => {
	it('dials the VM console on its own socket', async () => {
		render(Console, { vm });
		await vi.waitFor(() => expect(viewers).toHaveLength(1));
		expect((viewers[0].socket as FakeSocket).url).toMatch(/\/api\/vms\/team-a\/web\/vnc$/);
	});

	it('an ended session offers sign-in, not a redial', async () => {
		await drop(1008);
		expect(await screen.findByText('Your dotvirt session ended')).toBeTruthy();
		expect(screen.queryByRole('button', { name: 'Reconnect' })).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }));
		expect(fetchMock).toHaveBeenCalledWith('/api/me', expect.anything());
		expect(viewers).toHaveLength(1);
	});

	it('a lost connection offers a reconnect', async () => {
		await drop(1006);
		expect(await screen.findByText('Connection lost')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
		await vi.waitFor(() => expect(viewers).toHaveLength(2));
	});
});
