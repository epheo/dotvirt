import { describe, expect, it } from 'vitest';
import { ACCESS_UNKNOWN, authorBlock, editBlock, NOT_IN_GIT, READ_ONLY } from './authoring';

describe('editBlock', () => {
	it('lets an author edit a VM git holds', () => {
		expect(editBlock({ sourceFile: 'vms/web-1.yaml' }, 'yes')).toBe('');
	});
	it('names why an edit cannot stage', () => {
		expect(editBlock({ sourceFile: '' }, 'yes')).toBe(NOT_IN_GIT);
		expect(editBlock({ sourceFile: 'vms/web-1.yaml' }, 'no')).toBe(READ_ONLY);
		// Git first: adopting is the step that makes the VM editable at all.
		expect(editBlock({ sourceFile: '' }, 'no')).toBe(NOT_IN_GIT);
	});
	it('fails closed on an unknown verdict, and says it is unknown, not a denial', () => {
		expect(editBlock({ sourceFile: 'vms/web-1.yaml' }, 'unknown')).toBe(ACCESS_UNKNOWN);
		expect(authorBlock('unknown')).toBe('Your access could not be checked. Try again in a moment.');
		expect(authorBlock('no')).toBe(READ_ONLY);
		expect(authorBlock('yes')).toBe('');
	});
});
