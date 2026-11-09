import { describe, expect, it } from 'vitest';
import type { Template } from './api';
import { libraryLabel, templateKey, templatesFor } from './catalog';

const tpl = (name: string, library: string): Template => ({
	name,
	library,
	sourceFile: `templates/${name}.yaml`,
	yaml: '',
});

describe('templatesFor', () => {
	const all = [tpl('a', 'platform'), tpl('b', 'team-web'), tpl('c', 'team-db')];

	it('gives a project its own library and the shared one', () => {
		expect(templatesFor(all, 'team-web').map(templateKey)).toEqual(['platform/a', 'team-web/b']);
	});
	it('gives the Compute root every library', () => {
		expect(templatesFor(all)).toEqual(all);
	});
});

it('names the platform repo as the shared library', () => {
	expect(libraryLabel('platform')).toBe('Shared library');
	expect(libraryLabel('team-web')).toBe('team-web');
});
