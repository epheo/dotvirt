import { describe, expect, it } from 'vitest';
import type { DraftItem, VM } from '#lib/api.js';
import { fieldEdit, inlineBlock } from '#lib/inlineedit.js';

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

describe('fieldEdit', () => {
	it('sends the one field, cpu as a number', () => {
		expect(fieldEdit('cpuCores', '4')).toEqual({ cpuCores: 4 });
		expect(fieldEdit('memory', '8Gi')).toEqual({ memory: '8Gi' });
		expect(fieldEdit('power', 'Off')).toEqual({ power: 'Off' });
	});

	it('sends a value git leaves unset as its zero, which unstages the field', () => {
		expect(fieldEdit('cpuCores', undefined)).toEqual({ cpuCores: 0 });
		expect(fieldEdit('memory', undefined)).toEqual({ memory: '' });
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
