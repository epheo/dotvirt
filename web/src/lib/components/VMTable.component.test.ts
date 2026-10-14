import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import type { DraftItem, VM } from '#lib/api.js';
import VMTable from './VMTable.svelte';
import VMTableHost from '../../test/VMTableHost.svelte';

// The inventory grid: what the user filters, sorts, selects and opens. Sort
// and filter preferences persist, so each test starts from a clean store.

const vm = (name: string, over: Partial<VM> = {}): VM => ({
	namespace: 'team-a',
	name,
	power: 'On',
	sync: 'Synced',
	sourceFile: `vms/${name}.yaml`,
	phase: 'Running',
	...over,
});

const fleet = [
	vm('web', { guestIP: '10.0.0.5', cpuCores: 2, memory: '4Gi' }),
	vm('db', { namespace: 'team-b', power: 'Off', phase: 'Stopped', cpuCores: 8, memory: '16Gi' }),
	vm('cache', { sync: 'OutOfSync', cpuCores: 1, memory: '512Mi' }),
];

function setup(props: Partial<Parameters<typeof render<typeof VMTable>>[1]> = {}) {
	const onselect = vi.fn();
	const onstagedopen = vi.fn();
	const result = render(VMTable, {
		vms: fleet,
		onselect,
		staged: new Map<string, DraftItem>(),
		onstagedopen,
		...props,
	});
	return { ...result, onselect, onstagedopen };
}

// Row names in display order.
const names = () =>
	screen
		.getAllByRole('row')
		.slice(1)
		.map((r) => within(r).getAllByRole('link')[0].textContent);

beforeEach(() => {
	localStorage.clear();
});

describe('VMTable', () => {
	it('lists every VM sorted by name with its sizing', () => {
		setup();
		expect(names()).toEqual(['cache', 'db', 'web']);
		expect(screen.getByText('3 VMs')).toBeTruthy();
		const web = screen.getByText('web').closest('tr')!;
		expect(within(web).getByText('10.0.0.5')).toBeTruthy();
		expect(within(web).getByText('4Gi')).toBeTruthy();
	});

	it('sorts by a column, then reverses on a second click', async () => {
		setup();
		const byMemory = screen.getByRole('button', { name: 'Sort by Memory' });
		await fireEvent.click(byMemory);
		expect(names()).toEqual(['cache', 'web', 'db']);
		await fireEvent.click(byMemory);
		expect(names()).toEqual(['db', 'web', 'cache']);
	});

	it('remembers the sort across mounts', async () => {
		const first = setup();
		await fireEvent.click(screen.getByRole('button', { name: 'Sort by CPU' }));
		first.unmount();
		setup();
		expect(names()).toEqual(['cache', 'web', 'db']);
	});

	it('filters by search across name, namespace and IP', async () => {
		setup();
		const search = screen.getByPlaceholderText('Search name, namespace, IP…');
		await fireEvent.input(search, { target: { value: 'team-b' } });
		expect(names()).toEqual(['db']);
		await fireEvent.input(search, { target: { value: '10.0.0' } });
		expect(names()).toEqual(['web']);
		await fireEvent.input(search, { target: { value: 'nothing' } });
		expect(screen.getByText('No VMs match the current filters.')).toBeTruthy();
	});

	it('filters by power and sync state', async () => {
		setup();
		await fireEvent.change(screen.getByLabelText('Filter by power state'), {
			target: { value: 'Off' },
		});
		expect(names()).toEqual(['db']);
		await fireEvent.change(screen.getByLabelText('Filter by power state'), {
			target: { value: 'all' },
		});
		await fireEvent.change(screen.getByLabelText('Filter by ArgoCD sync status'), {
			target: { value: 'OutOfSync' },
		});
		expect(names()).toEqual(['cache']);
	});

	it('says so when the scope holds no VMs', () => {
		setup({ vms: [] });
		expect(screen.getByText('No VMs in scope.')).toBeTruthy();
	});

	it('opens a VM on row click, never on its checkbox', async () => {
		const { onselect } = setup();
		await fireEvent.click(screen.getByLabelText('Select web'));
		expect(onselect).not.toHaveBeenCalled();
		await fireEvent.click(screen.getByText('db').closest('tr')!);
		expect(onselect).toHaveBeenCalledWith(expect.objectContaining({ name: 'db' }));
	});

	it('select-all takes only the filtered rows', async () => {
		setup();
		await fireEvent.input(screen.getByPlaceholderText('Search name, namespace, IP…'), {
			target: { value: 'team-a' },
		});
		await fireEvent.click(screen.getByLabelText('Select all (filtered)'));
		const rows = screen.getAllByRole('row').slice(1);
		expect(rows.map((r) => r.getAttribute('aria-selected'))).toEqual(['true', 'true']);
		await fireEvent.input(screen.getByPlaceholderText('Search name, namespace, IP…'), {
			target: { value: '' },
		});
		expect(
			(screen.getByLabelText('Select db') as HTMLInputElement).checked,
			'a filtered-out row was selected',
		).toBe(false);
	});

	it('drops a selected VM that leaves the scope', async () => {
		const { rerender } = render(VMTableHost, { vms: fleet });
		await fireEvent.click(screen.getByLabelText('Select web'));
		await fireEvent.click(screen.getByLabelText('Select db'));
		expect(screen.getByRole('status').textContent).toBe('team-a/web,team-b/db');
		await rerender({ vms: fleet.filter((v) => v.name !== 'db') });
		expect(screen.getByRole('status').textContent).toBe('team-a/web');
	});

	it('moves focus with the arrow keys and toggles selection with space', async () => {
		const { onselect } = setup();
		const rows = screen.getAllByRole('row').slice(1);
		expect(rows.map((r) => r.tabIndex)).toEqual([0, -1, -1]);
		rows[0].focus();
		await fireEvent.keyDown(rows[0], { key: 'ArrowDown' });
		expect(document.activeElement).toBe(rows[1]);
		await fireEvent.keyDown(rows[1], { key: ' ' });
		expect(rows[1].getAttribute('aria-selected')).toBe('true');
		await fireEvent.keyDown(rows[1], { key: 'Enter' });
		expect(onselect).toHaveBeenCalledWith(expect.objectContaining({ name: 'db' }));
	});

	it('shows a staged change in place of the sync badge and opens it', async () => {
		const staged = new Map<string, DraftItem>([
			['team-a/web', { kind: 'delete', namespace: 'team-a', name: 'web', changes: [] }],
		]);
		const { onstagedopen, onselect } = setup({ staged });
		const web = screen.getByText('web');
		expect(web.closest('td')!.className).toContain('line-through');
		await fireEvent.click(within(web.closest('tr')!).getByText(/removal|Delete|Staged/i));
		expect(onstagedopen).toHaveBeenCalledWith(expect.objectContaining({ name: 'web' }));
		expect(onselect).not.toHaveBeenCalled();
	});

	it('hands the row to the context menu', async () => {
		const oncontextvm = vi.fn();
		setup({ oncontextvm });
		await fireEvent.contextMenu(screen.getByText('cache').closest('tr')!, {
			clientX: 5,
			clientY: 7,
		});
		expect(oncontextvm).toHaveBeenCalledWith(expect.objectContaining({ name: 'cache' }), 5, 7);
	});
});
