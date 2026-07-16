// Pure logic behind the Edit Settings wizard: seed an editable working copy
// from a VM, then diff it back into an EditRequest. No Svelte imports, so the
// seed/diff behavior is unit-testable.

import type { Disk, EditRequest, NIC, Power, VM } from '$lib/api';

// Devices carry a removed flag; rows added in the dialog are marked isNew so
// the diff can tell additions from removals of pre-existing devices.
export interface DiskRow extends Disk {
	removed: boolean;
	isNew: boolean;
}
export interface NicRow extends NIC {
	removed: boolean;
	isNew: boolean;
}

export interface EditForm {
	power: Power;
	cpuCores?: number;
	memory: string;
	instancetype: string;
	preference: string;
	// Sizing is one of two mutually-exclusive modes — KubeVirt forbids a VM from
	// carrying both an instancetype and inline cpu/memory. Seeded from how the VM
	// is sized today; the dialog's toggle lets the user convert between them.
	mode: 'instancetype' | 'custom';
	labelRows: { key: string; value: string }[];
	// Scheduling: the DRS opt-out annotation + the template's eviction strategy.
	drsExclude: boolean;
	evictionStrategy: string;
	// Disks: existing (with a removed flag) + newly added blank disks.
	disks: DiskRow[];
	nics: NicRow[];
}

export function seedEditForm(vm: VM): EditForm {
	return {
		power: vm.power,
		cpuCores: vm.cpuCores,
		memory: vm.memory ?? '',
		instancetype: vm.instancetype ?? '',
		preference: vm.preference ?? '',
		mode: vm.instancetype ? 'instancetype' : 'custom',
		labelRows: Object.entries(vm.labels ?? {}).map(([key, value]) => ({ key, value })),
		drsExclude: !!vm.drsExclude,
		evictionStrategy: vm.evictionStrategy ?? '',
		disks: (vm.disks ?? []).map((d) => ({ ...d, removed: false, isNew: false })),
		nics: (vm.networks ?? []).map((n) => ({ ...n, removed: false, isNew: false })),
	};
}

// Diff the form against the VM it was seeded from: only changed fields land in
// the request.
export function buildEditRequest(vm: VM, form: EditForm): EditRequest {
	const req: EditRequest = { sourceFile: vm.sourceFile };
	if (form.power !== vm.power) req.power = form.power;

	// CPU/memory and instance type are mutually exclusive (KubeVirt forbids both),
	// so send only the active mode's fields and tell the backend which to keep.
	const vmMode = vm.instancetype ? 'instancetype' : 'custom';
	const sizingChanged =
		form.mode !== vmMode ||
		(form.mode === 'custom' &&
			(form.cpuCores !== vm.cpuCores || form.memory !== (vm.memory ?? ''))) ||
		(form.mode === 'instancetype' && form.instancetype !== (vm.instancetype ?? ''));
	// A VM wrongly carrying inline cpu/memory under an instancetype must be
	// normalized even when nothing visibly changed — this heals a SyncFailed VM.
	const needsHeal = form.mode === 'instancetype' && (!!vm.cpuCores || !!vm.memory);
	if (sizingChanged || needsHeal) {
		if (form.mode === 'custom') {
			// Only convert to custom sizing when BOTH values are present. Sending
			// sizing:'custom' strips the instance type on the backend; without
			// replacement cpu/memory that leaves the VM unsized and the KubeVirt
			// webhook rejects it (re-creating the SyncFailed state this dialog fixes).
			if (form.cpuCores && form.memory) {
				req.sizing = 'custom';
				req.cpuCores = form.cpuCores;
				req.memory = form.memory;
			}
		} else if (form.instancetype) {
			req.sizing = 'instancetype';
			req.instancetype = form.instancetype;
		}
	}
	if (form.preference && form.preference !== (vm.preference ?? ''))
		req.preference = form.preference;

	// Scheduling: send only what changed ('' eviction strategy = cluster default).
	if (form.drsExclude !== !!vm.drsExclude) req.drsExclude = form.drsExclude;
	if (form.evictionStrategy !== (vm.evictionStrategy ?? ''))
		req.evictionStrategy = form.evictionStrategy;

	// Labels: upsert any changed/new, remove any deleted-from-original.
	const set: Record<string, string> = {};
	for (const r of form.labelRows) if (r.key.trim()) set[r.key.trim()] = r.value;
	const original = vm.labels ?? {};
	const setChanged: Record<string, string> = {};
	for (const [k, v] of Object.entries(set)) if (original[k] !== v) setChanged[k] = v;
	if (Object.keys(setChanged).length) req.setLabels = setChanged;
	const removedLabels = Object.keys(original).filter((k) => !(k in set));
	if (removedLabels.length) req.removeLabels = removedLabels;

	// Disks
	const addDisks = form.disks.filter((d) => d.isNew && !d.removed && d.name.trim());
	if (addDisks.length)
		req.addDisks = addDisks.map((d) => ({
			name: d.name,
			size: d.size ?? '10Gi',
			storageClass: d.storageClass || undefined,
		}));
	const removeDisks = form.disks.filter((d) => !d.isNew && d.removed).map((d) => d.name);
	if (removeDisks.length) req.removeDisks = removeDisks;

	// Networks
	const addNetworks = form.nics.filter((n) => n.isNew && !n.removed && n.network);
	if (addNetworks.length) req.addNetworks = addNetworks.map((n) => ({ name: n.network! }));
	const removeNetworks = form.nics.filter((n) => !n.isNew && n.removed).map((n) => n.name);
	if (removeNetworks.length) req.removeNetworks = removeNetworks;

	return req;
}
