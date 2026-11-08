import { describe, expect, it } from 'vitest';
import { diskNameError, freeName, interfaceName, networkAdds, RESERVED_DISKS } from './devices';

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
