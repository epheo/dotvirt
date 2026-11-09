import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { StorageClassInfo } from '#lib/api.js';
import ClassPick from './ClassPick.svelte';

const info = (name: string, extra: Partial<StorageClassInfo>) =>
	({ name, provisioner: 'r-drbd.io', ...extra }) as StorageClassInfo;

describe('ClassPick', () => {
	it('says what a replicated class promises, and only for one', () => {
		render(ClassPick, {
			label: 'Root disk class',
			value: '',
			onpick: vi.fn(),
			classes: [
				info('replicated', { replication: { copies: 2, tier: 'nvme' } }),
				info('local', { replication: { copies: 1, tier: 'nvme' } }),
				info('plain', { provisioner: 'other.csi' }),
			],
		});
		expect(screen.getByText(/2 copies, nvme: survives losing any one host/)).toBeTruthy();
		expect(screen.getByText(/One copy, nvme: lost with its host/)).toBeTruthy();
		expect(screen.getAllByText(/copies|One copy/)).toHaveLength(2);
	});
});
