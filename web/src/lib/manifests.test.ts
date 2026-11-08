import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
	api: {
		updateObjectManifest: vi.fn(),
		objectSpec: vi.fn(),
		updateTemplate: vi.fn(),
		templates: vi.fn(),
	},
}));
vi.mock('#lib/api.js', () => api);

import { objectManifestEdit, templateManifestEdit } from './manifests';

// The manifest editor sends back the version it loaded and re-reads the
// current one when git moved past it, for an object and for a template alike.

beforeEach(() => vi.clearAllMocks());

describe('objectManifestEdit', () => {
	const o = { resource: 'networkpolicy', namespace: 'team-a', name: 'web', baseBlob: 'b1' };

	it('stages with the version it was given and re-reads the object', async () => {
		const edit = objectManifestEdit(o);
		expect(edit.baseBlob).toBe('b1');
		await edit.onsubmit('kind: X\n', 'b1');
		expect(api.api.updateObjectManifest).toHaveBeenCalledWith(
			'networkpolicy',
			'team-a',
			'web',
			'kind: X\n',
			'b1',
		);
		api.api.objectSpec.mockResolvedValue({ manifest: 'kind: Y\n', baseBlob: 'b2' });
		await expect(edit.onreload()).resolves.toEqual({ yaml: 'kind: Y\n', baseBlob: 'b2' });
	});
});

describe('templateManifestEdit', () => {
	const t = {
		name: 'web',
		library: 'team-a',
		sourceFile: 'templates/web.yaml',
		yaml: 'a',
		baseBlob: 'b1',
	};

	it('stages with the version and re-reads the template from its library', async () => {
		const edit = templateManifestEdit(t);
		await edit.onsubmit('b', 'b1');
		expect(api.api.updateTemplate).toHaveBeenCalledWith({
			library: 'team-a',
			name: 'web',
			yaml: 'b',
			baseBlob: 'b1',
		});
		api.api.templates.mockResolvedValue({
			templates: [
				{ ...t, library: 'platform', yaml: 'other' },
				{ ...t, yaml: 'c', baseBlob: 'b2' },
			],
		});
		await expect(edit.onreload()).resolves.toEqual({ yaml: 'c', baseBlob: 'b2' });
	});

	it('says so when the template is gone', async () => {
		api.api.templates.mockResolvedValue({ templates: [] });
		await expect(templateManifestEdit(t).onreload()).rejects.toThrow(/no longer/);
	});
});
