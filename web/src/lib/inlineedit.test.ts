import { describe, expect, it } from 'vitest';
import type { DraftItem, VM } from '#lib/api.js';
import { composeEdit, inlineBlock } from '#lib/inlineedit.js';

const vm = (over: Partial<VM> = {}): VM => ({
	namespace: 'ns',
	name: 'web-1',
	power: 'On',
	sourceFile: 'vms/web-1.yaml',
	sync: 'Synced',
	cpuCores: 2,
	memory: '4Gi',
	...over,
});

describe('composeEdit', () => {
	it('adds the field to what is already staged', () => {
		expect(composeEdit({ memory: '8Gi' }, 'cpuCores', 4, 2)).toEqual({
			memory: '8Gi',
			cpuCores: 4,
		});
	});

	it('drops the field when it returns to the git value', () => {
		expect(composeEdit({ memory: '8Gi', cpuCores: 4 }, 'cpuCores', 2, 2)).toEqual({
			memory: '8Gi',
		});
	});

	it('stages nothing when the last field goes back', () => {
		expect(composeEdit({ cpuCores: 4 }, 'cpuCores', 2, 2)).toBeNull();
		expect(
			composeEdit({ cpuCores: 4, setLabels: {}, removeDisks: [] }, 'cpuCores', 2, 2),
		).toBeNull();
	});
});

describe('inlineBlock', () => {
	const staged = (over: Partial<DraftItem>): DraftItem => ({
		kind: 'edit',
		namespace: 'ns',
		name: 'web-1',
		changes: [],
		...over,
	});

	it('edits in place when nothing stands in the way', () => {
		expect(inlineBlock(vm(), 'cpuCores', null)).toBe('');
		expect(inlineBlock(vm(), 'memory', staged({ edit: { power: 'Off' } }))).toBe('');
	});

	it('names what blocks an in-place edit', () => {
		expect(inlineBlock(vm({ sourceFile: '' }), 'power', null)).toMatch(/adopt/);
		expect(inlineBlock(vm(), 'power', staged({ kind: 'delete' }))).toMatch(/staged delete/);
		expect(inlineBlock(vm(), 'power', staged({ yaml: 'kind: VirtualMachine' }))).toMatch(
			/staged edit/,
		);
		expect(inlineBlock(vm({ instancetype: 'u1.medium' }), 'cpuCores', null)).toMatch(/u1.medium/);
		expect(inlineBlock(vm({ instancetype: 'u1.medium' }), 'power', null)).toBe('');
	});
});
