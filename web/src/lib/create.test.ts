import { describe, expect, it } from 'vitest';
import type { Network, Options, StorageClassInfo, VM } from '#lib/api.js';
import {
	defaultUser,
	generatePassword,
	imageTally,
	liveMigrates,
	matchRanges,
	mostCommonSize,
	nextFreeName,
	networkLine,
	preferenceFor,
	searchNetworks,
	sharedClass,
	sizeLabel,
	usualNetworks,
	usualPreference,
	vmCountByNetwork,
} from '#lib/create.js';

function vm(name: string, over: Partial<VM> = {}): VM {
	return { namespace: 'web-prod', name, power: 'On', sourceFile: '', sync: 'Synced', ...over };
}

describe('nextFreeName', () => {
	it('continues the series most names follow', () => {
		const taken = ['web-1', 'web-2', 'web-412', 'db-7', 'scratch'];
		expect(nextFreeName(taken, 'web-prod')).toEqual({ name: 'web-413', after: 'web-412' });
	});
	it('keeps zero padding and skips a taken next', () => {
		expect(nextFreeName(['vm-007', 'vm-008', 'vm-009x'], 'x').name).toBe('vm-009');
		expect(nextFreeName(['vm-1', 'vm-3', 'vm-4'], 'x').name).toBe('vm-5');
	});
	it('starts a series when nothing is numbered', () => {
		expect(nextFreeName(['scratch'], 'web-prod')).toEqual({ name: 'web-prod-1', after: undefined });
		expect(nextFreeName(['web-prod-1'], 'web-prod').name).toBe('web-prod-2');
	});
});

const options: Options = {
	instancetypes: [
		{ name: 'u1.small', cpu: 1, memory: '2Gi' },
		{ name: 'u1.medium', cpu: 2, memory: '4Gi' },
		{ name: 'u1.large', cpu: 4, memory: '8Gi' },
	],
	preferences: [
		{ name: 'fedora' },
		{ name: 'rhel.9' },
		{ name: 'rhel.10' },
		{ name: 'centos.stream9' },
		{ name: 'windows.2k22' },
	],
	osImages: [],
	networks: [],
	storageClasses: [],
};

describe('mostCommonSize', () => {
	it('counts custom-sized VMs toward the matching instancetype', () => {
		const vms = [
			vm('a', { instancetype: 'u1.large' }),
			vm('b', { cpuCores: 2, memory: '4Gi' }),
			vm('c', { cpuCores: 2, memory: '4096Mi' }),
			vm('d', { cpuCores: 3, memory: '5Gi' }),
		];
		expect(mostCommonSize(vms, options)).toEqual({ it: options.instancetypes[1], count: 2 });
	});
	it('breaks a tie toward the larger size', () => {
		const vms = [vm('a', { instancetype: 'u1.small' }), vm('b', { instancetype: 'u1.large' })];
		expect(mostCommonSize(vms, options)!.it.name).toBe('u1.large');
	});
	it('has nothing to say about an empty project', () => {
		expect(mostCommonSize([], options)).toBeNull();
	});
});

describe('sizeLabel', () => {
	it('reads the size word and the numbers', () => {
		expect(sizeLabel({ name: 'u1.medium', cpu: 2, memory: '4Gi' })).toBe('Medium, 2 vCPU 4 GiB');
		expect(sizeLabel({ name: 'big', cpu: 16, memory: '64Gi' })).toBe('Big, 16 vCPU 64 GiB');
	});
});

describe('preferenceFor', () => {
	const p = options.preferences;
	it('takes the longest preference the image name starts with', () => {
		expect(preferenceFor('fedora-42', p)?.name).toBe('fedora');
		expect(preferenceFor('rhel-9.6', p)?.name).toBe('rhel.9');
		expect(preferenceFor('rhel-10', p)?.name).toBe('rhel.10');
		expect(preferenceFor('centos-stream-9', p)?.name).toBe('centos.stream9');
	});
	it('falls back to the OS family, then to nothing', () => {
		expect(preferenceFor('windows-2025', p)?.name).toBe('windows.2k22');
		expect(preferenceFor('alpine-3', p)).toBeUndefined();
	});
});

describe('defaultUser', () => {
	it('names the login the image family ships with', () => {
		expect(defaultUser('fedora-42')).toBe('fedora');
		expect(defaultUser('centos-stream-9')).toBe('cloud-user');
	});
});

const nets: Network[] = [
	{
		name: 'web-net',
		kind: 'default',
		scope: 'project',
		namespace: 'web-prod',
		subnets: ['10.40.0.0/16'],
		attachRef: 'web-prod/web-net',
		backing: 'UserDefinedNetwork',
	},
	{
		name: 'web-internal',
		kind: 'internal',
		scope: 'project',
		namespace: 'web-prod',
		attachRef: 'web-prod/web-internal',
		backing: 'UserDefinedNetwork',
	},
	{
		name: 'lan-101',
		kind: 'vlan',
		scope: 'shared',
		vlan: 101,
		namespaces: ['web-prod', 'db-prod'],
		attachRef: 'lan-101',
		backing: 'ClusterUserDefinedNetwork',
	},
	{
		name: 'db-net',
		kind: 'internal',
		scope: 'project',
		namespace: 'db-prod',
		attachRef: 'db-prod/db-net',
		backing: 'UserDefinedNetwork',
	},
];
const projectOf = (ns: string) => ({ 'web-prod': 'team-web', 'db-prod': 'team-db' })[ns] ?? '';

describe('vmCountByNetwork', () => {
	it('counts each VM once per network, the default NIC for the primary', () => {
		const vms = [
			vm('web-1', {
				networks: [
					{ name: 'default', network: 'pod' },
					{ name: 'a', network: 'lan-101' },
				],
			}),
			vm('web-2', {
				networks: [
					{ name: 'default', network: 'pod' },
					{ name: 'a', network: 'web-prod/web-internal' },
					{ name: 'b', network: 'web-prod/web-internal' },
				],
			}),
			vm('db-1', { namespace: 'db-prod', networks: [{ name: 'default', network: 'pod' }] }),
		];
		const c = vmCountByNetwork(vms, nets);
		expect(Object.fromEntries(c)).toEqual({
			'web-prod/web-net': 2,
			'lan-101': 1,
			'web-prod/web-internal': 1,
		});
	});
});

describe('networkLine', () => {
	it('says who a network serves', () => {
		expect(networkLine(nets[0], projectOf)).toBe('Project network of team-web');
		expect(networkLine(nets[2], projectOf)).toBe('VLAN network, VLAN 101, team-web and team-db');
	});
});

describe('searchNetworks', () => {
	const counts = new Map([
		['lan-101', 120],
		['web-prod/web-net', 389],
		['web-prod/web-internal', 12],
		['db-prod/db-net', 900],
	]);
	const opts = { project: 'team-web', projectOf, counts };
	it('puts the opener project first, then by VMs', () => {
		expect(searchNetworks(nets, 'web', opts).map((h) => [h.net.name, h.own, h.vms])).toEqual([
			['web-net', true, 389],
			['lan-101', true, 120],
			['web-internal', true, 12],
		]);
	});
	it('matches owners and subnets, and everything on an empty query', () => {
		expect(searchNetworks(nets, 'team-db', opts).map((h) => h.net.name)).toEqual([
			'lan-101',
			'db-net',
		]);
		expect(searchNetworks(nets, '10.40', opts).map((h) => h.net.name)).toEqual(['web-net']);
		expect(searchNetworks(nets, '', opts)).toHaveLength(4);
	});
});

describe('matchRanges', () => {
	it('finds every occurrence, case-insensitively', () => {
		expect(matchRanges('web-net of team-Web', 'web')).toEqual([
			{ start: 0, end: 3 },
			{ start: 16, end: 19 },
		]);
		expect(matchRanges('web', ' ')).toEqual([]);
	});
});

const cls = (name: string, over: Partial<StorageClassInfo> = {}): StorageClassInfo => ({
	name,
	provisioner: 'x',
	...over,
});

describe('sharedClass', () => {
	it('offers a shared class, the default first, then the freest', () => {
		const local = cls('lvms-vgslow', {
			default: true,
			profile: { accessModes: ['ReadWriteOnce'], recognized: true },
		});
		const nfs = cls('bulk-hdd', {
			profile: { accessModes: ['ReadWriteMany'], shared: true, recognized: true },
		});
		const ceph = cls('ceph', {
			free: 1e12,
			profile: { accessModes: ['ReadWriteMany'], shared: true, recognized: true },
		});
		expect(liveMigrates(local)).toBe(false);
		expect(sharedClass([local, nfs, ceph])?.name).toBe('ceph');
		expect(sharedClass([local])).toBeUndefined();
	});
});

describe('usual choices', () => {
	it('counts disks per boot image', () => {
		const disk = (image?: string) => ({ name: 'root', image });
		const vms = [
			vm('a', { disks: [disk('imgs/fedora-42'), disk()] }),
			vm('b', { disks: [disk('imgs/fedora-42')] }),
			vm('c', { disks: [disk('imgs/rhel-9.6')] }),
		];
		expect(Object.fromEntries(imageTally(vms))).toEqual({
			'imgs/fedora-42': 2,
			'imgs/rhel-9.6': 1,
		});
	});
	it('takes the preference most VMs carry', () => {
		const vms = [
			vm('a', { preference: 'rhel.9' }),
			vm('b', { preference: 'rhel.9' }),
			vm('c', { preference: 'fedora' }),
		];
		expect(usualPreference(vms)).toBe('rhel.9');
		expect(usualPreference([vm('x')])).toBe('');
	});
	it('picks secondary networks more than half the VMs attach', () => {
		const lan = { name: 'a', network: 'lan-101' };
		const vms = [
			vm('a', { networks: [lan] }),
			vm('b', { networks: [lan] }),
			vm('c', { networks: [{ name: 'b', network: 'web-prod/web-internal' }] }),
		];
		expect(usualNetworks(vms, nets)).toEqual(['lan-101']);
		expect(usualNetworks([], nets)).toEqual([]);
	});
	it('makes a fresh 16-character password each time', () => {
		const a = generatePassword();
		expect(a).toMatch(/^[a-zA-Z0-9]{16}$/);
		expect(generatePassword()).not.toBe(a);
	});
});
