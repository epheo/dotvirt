import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { openEdit } from './objects';
import { ui } from './state/ui.svelte';

// Edit reads the object back from git before its dialog opens: that read
// must neither report a sign-out as a failure nor open over a dialog the
// user opened while it was in flight.

const ref = { resource: 'networkpolicy', namespace: 'team-a', name: 'web' };
const spec = { ...ref, sourceFile: 'team-a/web.yaml', manifest: 'kind: NetworkPolicy\n' };
const json = (body: unknown) =>
	new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

beforeEach(() => ui.reset());
afterEach(() => vi.unstubAllGlobals());

describe('openEdit', () => {
	it('opens the manifest editor when the form cannot hold the object', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => json(spec)),
		);
		await openEdit(ref);
		expect(ui.modal).toMatchObject({ kind: 'editManifest', name: 'web', yaml: spec.manifest });
	});

	it('a sign-out raises no error toast', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => new Response('', { status: 401 })),
		);
		await openEdit(ref);
		expect(ui.toasts).toEqual([]);
		expect(ui.modal).toBeNull();
	});

	it('a dialog opened during the read stays', async () => {
		let answer!: (r: Response) => void;
		vi.stubGlobal(
			'fetch',
			vi.fn(() => new Promise<Response>((r) => (answer = r))),
		);
		const opening = openEdit(ref);
		ui.modal = { kind: 'propose', project: 'p' };
		answer(json(spec));
		await opening;
		expect(ui.modal).toEqual({ kind: 'propose', project: 'p' });
	});

	it('a failed read toasts', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => new Response('gone', { status: 404 })),
		);
		await openEdit(ref);
		expect(ui.toasts.map((t) => t.kind)).toEqual(['error']);
		expect(ui.modal).toBeNull();
	});
});
