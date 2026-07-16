import { describe, expect, it } from 'vitest';
import type { VM } from '$lib/api';
import { buildEditRequest, seedEditForm } from '$lib/editform';

function vm(over: Partial<VM> = {}): VM {
	return {
		namespace: 'ns-a',
		name: 'vm-a',
		power: 'On',
		sourceFile: 'vms/vm-a.yaml',
		sync: 'Synced',
		...over,
	};
}

const customVM = () =>
	vm({
		cpuCores: 2,
		memory: '4Gi',
		labels: { app: 'web' },
		disks: [{ name: 'rootdisk', type: 'dataVolume', size: '10Gi' }],
		networks: [{ name: 'default', network: 'pod' }],
	});

const itVM = () => vm({ instancetype: 'u1.medium', preference: 'fedora' });

describe('seedEditForm', () => {
	it('seeds mode from how the VM is sized today', () => {
		expect(seedEditForm(customVM()).mode).toBe('custom');
		expect(seedEditForm(itVM()).mode).toBe('instancetype');
	});

	it('marks seeded devices as pre-existing and not removed', () => {
		const form = seedEditForm(customVM());
		expect(form.disks).toEqual([
			{ name: 'rootdisk', type: 'dataVolume', size: '10Gi', removed: false, isNew: false },
		]);
		expect(form.nics).toEqual([{ name: 'default', network: 'pod', removed: false, isNew: false }]);
	});
});

describe('buildEditRequest', () => {
	it('yields only sourceFile for an untouched custom-sized VM', () => {
		const v = customVM();
		expect(buildEditRequest(v, seedEditForm(v))).toEqual({ sourceFile: v.sourceFile });
	});

	it('yields only sourceFile for an untouched instancetype VM', () => {
		const v = itVM();
		expect(buildEditRequest(v, seedEditForm(v))).toEqual({ sourceFile: v.sourceFile });
	});

	it('sends only power when only power changed', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.power = 'Off';
		expect(buildEditRequest(v, form)).toEqual({ sourceFile: v.sourceFile, power: 'Off' });
	});

	it('a cpu change sends the full custom sizing and nothing else', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.cpuCores = 4;
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			sizing: 'custom',
			cpuCores: 4,
			memory: '4Gi',
		});
	});

	it('a memory change sends the full custom sizing', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.memory = '8Gi';
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			sizing: 'custom',
			cpuCores: 2,
			memory: '8Gi',
		});
	});

	it('custom -> instancetype conversion sends the instancetype, never cpu/memory', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.mode = 'instancetype';
		form.instancetype = 'u1.small';
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			sizing: 'instancetype',
			instancetype: 'u1.small',
		});
	});

	it('custom -> instancetype without a chosen type sends no sizing at all', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.mode = 'instancetype';
		expect(buildEditRequest(v, form)).toEqual({ sourceFile: v.sourceFile });
	});

	it('instancetype -> custom conversion requires both cpu and memory', () => {
		const v = itVM();
		const form = seedEditForm(v);
		form.mode = 'custom';
		form.cpuCores = 2;
		// Memory still empty: sending sizing:'custom' now would strip the
		// instancetype and leave the VM unsized, so nothing may be sent.
		expect(buildEditRequest(v, form)).toEqual({ sourceFile: v.sourceFile });
		form.memory = '4Gi';
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			sizing: 'custom',
			cpuCores: 2,
			memory: '4Gi',
		});
	});

	it('heals a VM carrying inline cpu/memory under an instancetype without visible edits', () => {
		const v = vm({ instancetype: 'u1.medium', cpuCores: 2 });
		expect(buildEditRequest(v, seedEditForm(v))).toEqual({
			sourceFile: v.sourceFile,
			sizing: 'instancetype',
			instancetype: 'u1.medium',
		});
	});

	it('label add lands in setLabels only', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.labelRows.push({ key: 'tier', value: 'db' });
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			setLabels: { tier: 'db' },
		});
	});

	it('label removal lands in removeLabels only', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.labelRows = form.labelRows.filter((r) => r.key !== 'app');
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			removeLabels: ['app'],
		});
	});

	it('a changed label value upserts without a removal', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.labelRows = [{ key: 'app', value: 'db' }];
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			setLabels: { app: 'db' },
		});
	});

	it('blank label keys are ignored, entered keys are trimmed', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.labelRows.push({ key: '   ', value: 'x' }, { key: ' tier ', value: 'db' });
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			setLabels: { tier: 'db' },
		});
	});

	it('disk add and remove land in addDisks/removeDisks', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.disks[0].removed = true;
		form.disks.push({
			name: 'data',
			size: '20Gi',
			storageClass: 'fast',
			removed: false,
			isNew: true,
		});
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			addDisks: [{ name: 'data', size: '20Gi', storageClass: 'fast' }],
			removeDisks: ['rootdisk'],
		});
	});

	it('an added disk defaults to 10Gi and the cluster default class', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.disks.push({ name: 'data', removed: false, isNew: true });
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			addDisks: [{ name: 'data', size: '10Gi', storageClass: undefined }],
		});
	});

	it('a new disk that was removed again, or left unnamed, sends nothing', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.disks.push(
			{ name: 'gone', removed: true, isNew: true },
			{ name: '  ', removed: false, isNew: true },
		);
		expect(buildEditRequest(v, form)).toEqual({ sourceFile: v.sourceFile });
	});

	it('nic add and remove land in addNetworks/removeNetworks', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.nics[0].removed = true;
		form.nics.push({ name: '', network: 'ns-a/net-a', removed: false, isNew: true });
		expect(buildEditRequest(v, form)).toEqual({
			sourceFile: v.sourceFile,
			addNetworks: [{ name: 'ns-a/net-a' }],
			removeNetworks: ['default'],
		});
	});

	it('a new nic without a network sends nothing', () => {
		const v = customVM();
		const form = seedEditForm(v);
		form.nics.push({ name: '', removed: false, isNew: true });
		expect(buildEditRequest(v, form)).toEqual({ sourceFile: v.sourceFile });
	});
});
