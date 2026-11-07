import { beforeEach, describe, expect, it, vi } from 'vitest';
import { tick } from 'svelte';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import type { DraftItem, VM } from '#lib/api.js';
import { inventory } from '#lib/state/inventory.svelte.js';
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
	vm('web', { guestIP: '10.0.0.5', cpuCores: 2, memory: '4Gi', nodeName: 'worker-1' }),
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

// Row names in display order; group headings carry no link.
const names = () =>
	screen
		.getAllByRole('row')
		.slice(1)
		.flatMap((r) => within(r).queryAllByRole('link').slice(0, 1))
		.map((a) => a.textContent);

beforeEach(() => {
	localStorage.clear();
	inventory.inventory = null;
});

describe('VMTable', () => {
	it('lists every VM, exceptions first, with its host and sizing in real units', () => {
		setup();
		expect(names()).toEqual(['cache', 'db', 'web']);
		expect(screen.getByRole('button', { name: /^All/ }).textContent).toContain('3');
		const web = screen.getByText('web').closest('tr')!;
		expect(within(web).getByText('10.0.0.5')).toBeTruthy();
		expect(within(web).getByText('worker-1')).toBeTruthy();
		expect(within(web).getByText('4 GiB')).toBeTruthy();
	});

	it('says only the run state for a healthy VM and names the exception otherwise', () => {
		setup();
		const web = screen.getByText('web').closest('tr')!;
		expect(within(web).getByText('Running')).toBeTruthy();
		expect(within(web).queryByText(/Synced|Healthy/)).toBeNull();
		const cache = screen.getByText('cache').closest('tr')!;
		expect(within(cache).getByText('Differs from git')).toBeTruthy();
	});

	it('counts and floats VMs with issues from the same list as the header', async () => {
		const broken = vm('zeta', { syncError: 'webhook denied' });
		inventory.inventory = {
			projects: [{ name: 'p', namespaces: [{ namespace: 'team-a', vms: [broken] }] }],
		};
		setup({ vms: [...fleet, broken] });
		expect(names()[0]).toBe('zeta');
		// Under the default sort the issue rows sit under their own heading.
		expect(screen.getByText('With issues (1), always on top')).toBeTruthy();
		expect(screen.getByText('Everything else (3)')).toBeTruthy();
		const zeta = screen.getByText('zeta').closest('tr')!;
		expect(within(zeta).getByRole('button', { name: 'See why' })).toBeTruthy();
		const chip = screen.getByRole('button', { name: /^With issues/ });
		expect(chip.textContent).toContain('1');
		await fireEvent.click(chip);
		expect(names()).toEqual(['zeta']);
		expect(within(screen.getByText('zeta').closest('tr')!).getByText('Apply refused')).toBeTruthy();
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
		await fireEvent.click(screen.getByRole('button', { name: 'Sort by vCPU' }));
		first.unmount();
		setup();
		expect(names()).toEqual(['cache', 'web', 'db']);
	});

	it('filters by search across name, namespace, IP and host', async () => {
		setup();
		const search = screen.getByPlaceholderText('Filter by name, IP or host');
		await fireEvent.input(search, { target: { value: 'team-b' } });
		expect(names()).toEqual(['db']);
		await fireEvent.input(search, { target: { value: '10.0.0' } });
		expect(names()).toEqual(['web']);
		await fireEvent.input(search, { target: { value: 'worker-1' } });
		expect(names()).toEqual(['web']);
		await fireEvent.input(search, { target: { value: 'nothing' } });
		expect(screen.getByText('No VMs match the current filter.')).toBeTruthy();
	});

	it('filters with the state chips and remembers the choice', async () => {
		const first = setup();
		await fireEvent.click(screen.getByRole('button', { name: /^Stopped/ }));
		expect(names()).toEqual(['db']);
		first.unmount();
		setup();
		expect(names()).toEqual(['db']);
		await fireEvent.click(screen.getByRole('button', { name: /^All/ }));
		expect(names()).toEqual(['cache', 'db', 'web']);
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
		await fireEvent.input(screen.getByPlaceholderText('Filter by name, IP or host'), {
			target: { value: 'team-a' },
		});
		await fireEvent.click(screen.getByLabelText('Select this page'));
		const rows = screen.getAllByRole('row').slice(1);
		expect(rows.map((r) => r.getAttribute('aria-selected'))).toEqual(['true', 'true']);
		await fireEvent.input(screen.getByPlaceholderText('Filter by name, IP or host'), {
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

	it('shows a staged change in its own column and opens it', async () => {
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

	describe('at fleet scale', () => {
		const many = Array.from({ length: 120 }, (_, i) =>
			vm(`vm-${String(i).padStart(3, '0')}`, { nodeName: 'worker-1' }),
		);
		const rowsShown = () => screen.getAllByRole('row').slice(1);

		it('shows one page and says where it sits', async () => {
			setup({ vms: many });
			expect(rowsShown()).toHaveLength(50);
			expect(screen.getByRole('navigation', { name: 'Pages' }).textContent).toContain(
				'Showing 1-50 of 120',
			);
			await fireEvent.click(screen.getByRole('button', { name: 'Next page' }));
			expect(names()[0]).toBe('vm-050');
			expect(screen.getByText('Page 2 of 3')).toBeTruthy();
		});

		it('starts over at page 1 when the filter changes', async () => {
			setup({ vms: many });
			await fireEvent.click(screen.getByRole('button', { name: 'Next page' }));
			await fireEvent.input(screen.getByPlaceholderText('Filter by name, IP or host'), {
				target: { value: 'vm-1' },
			});
			expect(names()[0]).toBe('vm-100');
			expect(screen.getByRole('navigation', { name: 'Pages' }).textContent).toContain(
				'Showing 1-20 of 20',
			);
		});

		it('opens on the page of the peeked row', () => {
			setup({ vms: many, activeKey: 'team-a/vm-075' });
			expect(names()).toContain('vm-075');
			expect(screen.getByText('Page 2 of 3')).toBeTruthy();
		});

		it('selects a page, then every row on request', async () => {
			render(VMTableHost, { vms: many });
			await fireEvent.click(screen.getByLabelText('Select this page'));
			expect(screen.getByRole('status').textContent!.split(',')).toHaveLength(50);
			await fireEvent.click(screen.getByRole('button', { name: 'Select all 120' }));
			expect(screen.getByRole('status').textContent!.split(',')).toHaveLength(120);
			await fireEvent.click(screen.getByRole('button', { name: 'Clear selection' }));
			expect(screen.getByRole('status').textContent).toBe('');
		});

		it('walks the arrow keys across a page boundary', async () => {
			setup({ vms: many });
			const last = rowsShown()[49];
			last.focus();
			await fireEvent.keyDown(last, { key: 'ArrowDown' });
			await tick();
			expect(screen.getByText('Page 2 of 3')).toBeTruthy();
			expect((document.activeElement as HTMLElement).dataset.key).toBe('team-a/vm-050');
		});
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
