import { describe, expect, it } from 'vitest';
import type { Network, Options, Template } from './api';
import { catalogContext, catalogRows } from './catalog';

const options = (over: Partial<Options> = {}): Options => ({
	instancetypes: [{ name: 'u1.medium', cpu: 2, memory: '4Gi' }],
	preferences: [{ name: 'fedora' }, { name: 'windows.2k25', minCPU: 2, minMemory: '4Gi' }],
	osImages: [{ name: 'fedora-42', namespace: 'images', ready: true }],
	networks: [],
	storageClasses: [{ name: 'fast', default: true }],
	...over,
});

const tpl = (name: string, library: string): Template => ({
	name,
	library,
	sourceFile: `templates/${name}.yaml`,
	yaml: '',
});

const net = (n: Partial<Network> & { name: string }): Network => ({
	kind: 'internal',
	scope: 'project',
	backing: 'UserDefinedNetwork',
	...n,
});

describe('catalogContext', () => {
	it('counts what new VMs are made from', () => {
		const t = [tpl('a', 'platform'), tpl('b', 'platform'), tpl('c', 'team-web')];
		expect(catalogContext(t, options())).toBe(
			'3 templates in 2 libraries, 1 boot image, 1 instance type',
		);
	});
	it('leaves out a source still loading rather than counting it as none', () => {
		expect(catalogContext(null, options())).toBe('1 boot image, 1 instance type');
		expect(catalogContext([tpl('a', 'platform')], null)).toBe('1 template');
	});
});

describe('catalogRows', () => {
	it('tells loading from empty', () => {
		expect(catalogRows('templates', null, options())).toBeNull();
		expect(catalogRows('images', [], null)).toBeNull();
		expect(catalogRows('networks', [], options())).toEqual([]);
	});

	it('says a size in vCPU and memory, and a preference by what it needs', () => {
		expect(catalogRows('instancetypes', [], options())![0].fact).toBe('2 vCPU, 4 GiB');
		expect(catalogRows('preferences', [], options())!.map((r) => r.fact)).toEqual([
			'No minimum size',
			'needs 2 vCPU, 4 GiB',
		]);
	});

	it('groups a shared network into one row, keyed as its page is', () => {
		const networks = [
			net({ name: 'lan-101', scope: 'shared', kind: 'vlan', namespaces: ['web', 'db'] }),
			net({ name: 'web-net', namespace: 'web', attachRef: 'web/web-net' }),
			// Same name as the shared one, in another namespace: not its owner.
			net({ name: 'lan-101', namespace: 'lab', attachRef: 'lab/lan-101' }),
		];
		const rows = catalogRows(
			'networks',
			[],
			options({
				networks: [
					{ name: 'lan-101', namespace: 'web' },
					{ name: 'lan-101', namespace: 'db' },
					{ name: 'web-net', namespace: 'web' },
					{ name: 'lan-101', namespace: 'lab' },
					{ name: 'hand-made', namespace: 'web' },
				],
			}),
			networks,
		)!;
		expect(rows.map((r) => [r.key, r.fact])).toEqual([
			['lan-101', 'VLAN network, in 2 namespaces'],
			['web/web-net', 'Project network, in web'],
			['lab/lan-101', 'Project network, in lab'],
			['web/hand-made', 'Network attachment, in web'],
		]);
		expect(rows[3].network?.known).toBeUndefined();
	});
});
