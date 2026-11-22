import { describe, expect, it } from 'vitest';
import {
	diskNameError,
	freeName,
	interfaceName,
	lostWithVM,
	networkAdds,
	RESERVED_DISKS,
} from './devices';

describe('freeName', () => {
	it('takes the first number not already used', () => {
		expect(freeName('disk-', [])).toBe('disk-1');
		// A removal leaves a gap; the next add fills it instead of repeating disk-3.
		expect(freeName('disk-', ['rootdisk', 'disk-1', 'disk-3'])).toBe('disk-2');
		expect(freeName('disk', [...RESERVED_DISKS, 'disk1', 'disk2'])).toBe('disk3');
	});
});

describe('diskNameError', () => {
	it('refuses a name the VM already uses or the API would reject', () => {
		expect(diskNameError('disk-2', ['rootdisk', 'disk-1'])).toBe('');
		expect(diskNameError('disk-1', ['rootdisk', 'disk-1'])).toMatch(/already a disk/);
		expect(diskNameError('Data_1', [])).toMatch(/lowercase/);
	});
});

describe('interfaceName', () => {
	it("is the network ref's last segment, as the server derives it", () => {
		expect(interfaceName('web-prod/web-net')).toBe('web-net');
		expect(interfaceName('lan-101')).toBe('lan-101');
	});
});

describe('networkAdds', () => {
	it("keeps the server's default name when it is free", () => {
		expect(networkAdds(['web-prod/web-net'], ['default'])).toEqual([{ name: 'web-prod/web-net' }]);
	});
	it('names the interface itself when the default is taken, by the VM or another add', () => {
		expect(networkAdds(['team-a/lan', 'lan'], ['default', 'lan'])).toEqual([
			{ name: 'team-a/lan', interface: 'lan-2' },
			{ name: 'lan', interface: 'lan-3' },
		]);
	});
});

describe('lostWithVM', () => {
	it('names the disks the VM provisioned itself, never a claim it only references', () => {
		expect(
			lostWithVM({
				disks: [
					{ name: 'rootdisk', type: 'dataVolume', size: '30Gi', ownedByVM: true },
					{ name: 'cloudinitdisk', type: 'cloudInitNoCloud' },
					{ name: 'shared', type: 'pvc', claim: 'team-share' },
				],
			}),
		).toBe('Its disk rootdisk (30 GiB) is deleted with it, and the data on it.');
		expect(
			lostWithVM({
				disks: [
					{ name: 'rootdisk', size: '30Gi', ownedByVM: true },
					{ name: 'data', size: '100Gi', ownedByVM: true },
					{ name: 'logs', ownedByVM: true },
				],
			}),
		).toBe(
			'Its disks rootdisk (30 GiB), data (100 GiB) and logs are deleted with it, and the data on them.',
		);
		expect(lostWithVM({ disks: [{ name: 'shared', type: 'pvc' }] })).toBe('');
		expect(lostWithVM({})).toBe('');
	});
});
