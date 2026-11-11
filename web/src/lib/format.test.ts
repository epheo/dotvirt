import { describe, expect, it } from 'vitest';
import { plural, serverReason } from './format.js';

describe('plural', () => {
	it('keeps the noun singular for one', () => {
		expect(plural(1, 'host')).toBe('1 host');
	});

	it('groups thousands and pluralizes the rest', () => {
		expect(plural(0, 'VM')).toBe('0 VMs');
		expect(plural(2846, 'VM')).toBe('2,846 VMs');
	});

	it('takes an irregular plural', () => {
		expect(plural(3, 'class', 'classes')).toBe('3 classes');
	});
});

describe('serverReason', () => {
	it('drops the kind and keeps the sentence', () => {
		expect(serverReason('forbidden: pull request #8 changed outside the console\n')).toBe(
			'pull request #8 changed outside the console',
		);
		expect(serverReason('conflict: the branch moved')).toBe('the branch moved');
		expect(serverReason('the forge is down')).toBe('the forge is down');
	});
});
