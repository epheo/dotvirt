import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { ui } from '#lib/state/ui.svelte.js';
import StageModal from './StageModal.svelte';
import Wizard from './Wizard.svelte';

// A staging dialog reports what the stage call returned when its host asks:
// a stage that left nothing staged must not claim it staged.

const body = createRawSnippet(() => ({ render: () => '<p>form</p>' }));
const view = { base: 'main', branch: 'd', count: 0, items: [] };

beforeEach(() => ui.reset());

describe('StageModal', () => {
	it('hands the returned draft to onresult instead of the staged toast', async () => {
		const onresult = vi.fn();
		render(StageModal, {
			title: 'Stage',
			label: 'Stage it',
			onsubmit: async () => view,
			onresult,
			onclose: vi.fn(),
			children: body,
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Stage it' }));
		await vi.waitFor(() => expect(onresult).toHaveBeenCalledWith(view));
		expect(ui.toasts).toEqual([]);
	});

	it('raises the staged toast without one', async () => {
		render(StageModal, {
			title: 'Stage',
			label: 'Stage it',
			onsubmit: async () => view,
			onclose: vi.fn(),
			children: body,
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Stage it' }));
		await vi.waitFor(() => expect(ui.toasts).toHaveLength(1));
	});
});

describe('Wizard', () => {
	it('hands the returned draft to onresult instead of the staged toast', async () => {
		const onresult = vi.fn();
		render(Wizard, {
			title: 'Edit',
			steps: [{ title: 'One', body }],
			canFinish: true,
			finishLabel: 'Stage change',
			onsubmit: async () => view,
			onresult,
			onclose: vi.fn(),
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Stage change' }));
		await vi.waitFor(() => expect(onresult).toHaveBeenCalledWith(view));
		expect(ui.toasts).toEqual([]);
	});
});
