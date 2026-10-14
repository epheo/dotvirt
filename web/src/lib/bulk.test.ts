import { describe, expect, it } from 'vitest';
import { bulkOutcome } from './bulk';

const vm = (name: string) => ({ namespace: 'ns', name });

describe('bulkOutcome', () => {
	it('keeps only the failed VMs picked and names the first error', () => {
		const out = bulkOutcome(
			'Deletion staged for',
			3,
			[vm('a'), vm('b')],
			[
				{ status: 'fulfilled', value: undefined },
				{ status: 'rejected', reason: new Error('Error: 409 conflict') },
			],
		);
		expect([...out.keep]).toEqual(['ns/b']);
		expect(out.staged).toBe(1);
		expect(out.failed).toBe(1);
		expect(out.msg).toBe('Deletion staged for 1 of 3 (1 skipped, 1 failed). b: 409 conflict');
	});

	it('clears the selection when everything staged', () => {
		const out = bulkOutcome('Powered on: staged', 1, [vm('a')], [
			{ status: 'fulfilled', value: undefined },
		]);
		expect(out.keep.size).toBe(0);
		expect(out.msg).toBe('Powered on: staged 1 of 1.');
	});
});
