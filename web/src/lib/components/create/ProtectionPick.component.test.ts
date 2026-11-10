import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { Modules, StorageClass, StorageClassInfo } from '#lib/api.js';
import { modules } from '#lib/state/modules.svelte.js';
import ProtectionPick from './ProtectionPick.svelte';

const info = (name: string, copies?: number, extra: Partial<StorageClassInfo> = {}) =>
	({
		name,
		provisioner: copies ? 'r-drbd.io' : 'other.csi',
		...(copies ? { replication: { copies, tier: 'nvme' } } : {}),
		...extra,
	}) as StorageClassInfo;
const classes = [info('r-drbd-local', 1), info('r-drbd-replicated', 2), info('plain')];
const served = (replicatedStorage: boolean) => ({ replicatedStorage }) as Modules;

function mount(list: (StorageClassInfo | StorageClass)[] = classes) {
	const onpick = vi.fn();
	render(ProtectionPick, { disk: 'Root disk', value: '', onpick, classes: list });
	return onpick;
}
const cardsShown = () => screen.queryByText('Root disk: how much should it survive?') !== null;

describe('ProtectionPick', () => {
	afterEach(() => {
		modules.value = null;
	});

	it('asks how much the disk should survive where r-drbd serves', async () => {
		modules.value = served(true);
		const onpick = mount();
		expect(cardsShown()).toBe(true);
		expect(screen.getByRole('radio', { name: /^One copy/ })).toBeTruthy();
		// No class asks three copies: the card says how to get one.
		expect(
			(screen.getByRole('radio', { name: /^Three copies/ }) as HTMLInputElement).disabled,
		).toBe(true);
		expect(screen.getByText(/No class you can use keeps 3 copies/)).toBeTruthy();
		await fireEvent.click(screen.getByRole('radio', { name: /^Two copies/ }));
		expect(onpick).toHaveBeenCalledWith('r-drbd-replicated');
		// A class outside r-drbd stays pickable below.
		expect(screen.getByText('Other classes')).toBeTruthy();
	});

	it('keeps the class list while the module read is unanswered', () => {
		mount();
		expect(cardsShown()).toBe(false);
		expect(screen.getByText('Root disk class')).toBeTruthy();
	});

	it('keeps the class list where r-drbd is not served', () => {
		modules.value = served(false);
		mount();
		expect(cardsShown()).toBe(false);
	});

	it('keeps the class list for a caller who may not list classes', () => {
		modules.value = served(true);
		// Without list on storageclasses only the names reach the form.
		mount([{ name: 'r-drbd-replicated' }, { name: 'plain', default: true }]);
		expect(cardsShown()).toBe(false);
	});
});
