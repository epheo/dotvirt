import { describe, expect, it } from 'vitest';
import { editBlock, NOT_IN_GIT, READ_ONLY } from './authoring';

describe('editBlock', () => {
	it('lets an author edit a VM git holds', () => {
		expect(editBlock({ sourceFile: 'vms/web-1.yaml' }, true)).toBe('');
	});
	it('names why an edit cannot stage', () => {
		expect(editBlock({ sourceFile: '' }, true)).toBe(NOT_IN_GIT);
		expect(editBlock({ sourceFile: 'vms/web-1.yaml' }, false)).toBe(READ_ONLY);
		// Git first: adopting is the step that makes the VM editable at all.
		expect(editBlock({ sourceFile: '' }, false)).toBe(NOT_IN_GIT);
	});
});
