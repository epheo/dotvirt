import { describe, expect, it } from 'vitest';
import { share } from './share';

const vm = (name: string, power = 'On') => ({
	name,
	namespace: 'web',
	power,
	disks: [{ name: 'root', size: '10Gi' }],
});
const frame = (vms: ReturnType<typeof vm>[]) => ({
	projects: [{ name: 'p', namespaces: [{ namespace: 'web', vms }] }],
	tasksVersion: 1,
});
// Each frame is a fresh parse off the wire, never shared with the previous one.
const wire = <T>(x: T): T => JSON.parse(JSON.stringify(x));

describe('share', () => {
	it('returns prev itself for an identical frame', () => {
		const prev = wire(frame([vm('a'), vm('b')]));
		expect(share(prev, wire(prev))).toBe(prev);
	});

	it('reuses unchanged VMs and replaces only the changed path', () => {
		const prev = wire(frame([vm('a'), vm('b')]));
		const out = share(prev, wire(frame([vm('a'), vm('b', 'Off')])));
		const [pa, pb] = prev.projects[0].namespaces[0].vms;
		const [oa, ob] = out.projects[0].namespaces[0].vms;
		expect(out).not.toBe(prev);
		expect(out.projects[0]).not.toBe(prev.projects[0]);
		expect(oa).toBe(pa);
		expect(ob).not.toBe(pb);
		expect(ob.power).toBe('Off');
		expect(ob.disks).toBe(pb.disks);
	});

	it('pairs VMs by identity across inserts and reorders', () => {
		const prev = wire(frame([vm('a'), vm('b')]));
		const out = share(prev, wire(frame([vm('0'), vm('b'), vm('a')])));
		const [pa, pb] = prev.projects[0].namespaces[0].vms;
		const vms = out.projects[0].namespaces[0].vms;
		expect(vms.map((v) => v.name)).toEqual(['0', 'b', 'a']);
		expect(vms[1]).toBe(pb);
		expect(vms[2]).toBe(pa);
	});

	it('drops removed elements and keys', () => {
		const prev = wire({ ...frame([vm('a'), vm('b')]), warnings: ['x'] });
		const next = wire(frame([vm('a')])) as typeof prev;
		const out = share(prev, next);
		expect(out).not.toBe(prev);
		expect('warnings' in out).toBe(false);
		expect(out.projects[0].namespaces[0].vms).toHaveLength(1);
		expect(out.projects[0].namespaces[0].vms[0]).toBe(prev.projects[0].namespaces[0].vms[0]);
	});

	it('falls back to position when identities collide', () => {
		const prev = wire([
			{ name: 'x', v: 1 },
			{ name: 'x', v: 2 },
		]);
		const out = share(
			prev,
			wire([
				{ name: 'x', v: 1 },
				{ name: 'x', v: 3 },
			]),
		);
		expect(out[0]).toBe(prev[0]);
		expect(out[1]).toEqual({ name: 'x', v: 3 });
	});

	it('takes next when there is no previous frame', () => {
		const next = wire(frame([vm('a')]));
		expect(share(null, next)).toBe(next);
	});
});
