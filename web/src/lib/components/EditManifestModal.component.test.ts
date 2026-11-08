import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { HttpError } from '#lib/api.js';
import { ui } from '#lib/state/ui.svelte.js';
import EditManifestModal from './EditManifestModal.svelte';

// A manifest edit names the version it was made on, so the server refuses it
// once git moved past that version; the refusal leads to the current version
// with the user's edit kept beside it.

const loaded = 'kind: NetworkPolicy\nspec: {}\n';
const stale = new HttpError(
	'/api/objects/networkpolicy/team-a/web: 409 conflict: team-a/web changed on main since the editor loaded it; reload it and make the edit again\n',
	409,
);

function setup(onsubmit: (yaml: string, baseBlob?: string) => Promise<unknown>) {
	const onreload = vi.fn(async () => ({
		yaml: 'kind: NetworkPolicy\nspec: {a: 1}\n',
		baseBlob: 'b2',
	}));
	render(EditManifestModal, {
		title: 'Edit manifest',
		sourceFile: 'team-a/web.yaml',
		yaml: loaded,
		baseBlob: 'b1',
		summary: 'Replaces team-a/web.yaml',
		onsubmit,
		onreload,
		onclose: vi.fn(),
	});
	return { onreload, editor: screen.getByLabelText('Manifest') as HTMLTextAreaElement };
}

beforeEach(() => ui.reset());

describe('EditManifestModal', () => {
	it('sends the version it loaded with the edit', async () => {
		const onsubmit = vi.fn(async () => {});
		const { editor } = setup(onsubmit);
		await fireEvent.input(editor, { target: { value: 'kind: NetworkPolicy\nspec: {b: 2}\n' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Stage edit' }));
		expect(onsubmit).toHaveBeenCalledWith('kind: NetworkPolicy\nspec: {b: 2}\n', 'b1');
	});

	it('a version git moved past reloads, keeps the edit in view, and stages on the new one', async () => {
		const onsubmit = vi.fn<(yaml: string, baseBlob?: string) => Promise<unknown>>();
		onsubmit.mockRejectedValueOnce(stale).mockResolvedValueOnce(undefined);
		const { onreload, editor } = setup(onsubmit);
		await fireEvent.input(editor, { target: { value: 'kind: NetworkPolicy\nspec: {b: 2}\n' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Stage edit' }));

		await fireEvent.click(await screen.findByRole('button', { name: 'Reload and re-apply' }));
		await vi.waitFor(() => expect(editor.value).toBe('kind: NetworkPolicy\nspec: {a: 1}\n'));
		expect(onreload).toHaveBeenCalledOnce();
		expect(screen.getByText('+ spec: {b: 2}')).toBeTruthy();
		expect(screen.queryByRole('button', { name: 'Reload and re-apply' })).toBeNull();

		await fireEvent.input(editor, {
			target: { value: 'kind: NetworkPolicy\nspec: {a: 1, b: 2}\n' },
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Stage edit' }));
		expect(onsubmit).toHaveBeenLastCalledWith('kind: NetworkPolicy\nspec: {a: 1, b: 2}\n', 'b2');
	});

	it('another refusal offers no reload', async () => {
		const onsubmit = vi.fn(async () => {
			throw new HttpError(
				'/api/objects/x: 409 conflict: team-a/web has a staged delete waiting',
				409,
			);
		});
		const { editor } = setup(onsubmit);
		await fireEvent.input(editor, { target: { value: 'kind: NetworkPolicy\n' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Stage edit' }));
		await screen.findByText(/has a staged delete waiting/);
		expect(screen.queryByRole('button', { name: 'Reload and re-apply' })).toBeNull();
	});
});
