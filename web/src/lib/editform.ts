// Pure logic behind the Edit Settings wizard: seed an editable working copy
// from a VM, then diff it back into an EditRequest. No Svelte imports, so the
// seed/diff behavior is unit-testable.

import type { Disk, EditRequest, NIC, PlacementGroup, Power, VM } from '$lib/api';

// The Edit Settings sections a Configure card (or context menu) can deep-link
// to — one vocabulary for the dialog, its openers, and the wizard step map.
export type EditSection = 'compute' | 'scheduling' | 'storage' | 'network' | 'labels';

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
interface GroupRow extends PlacementGroup {
	removed: boolean;
	isNew: boolean;
}

interface EditForm {
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
	// Placement: groups (with a removed flag) + the pinned-host list.
	groups: GroupRow[];
	pin: string[];
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
		groups: (vm.scheduling?.groups ?? []).map((g) => ({ ...g, removed: false, isNew: false })),
		pin: [...(vm.scheduling?.pin ?? [])],
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

	// Placement — never diffed for a custom-affinity VM (the form is read-only
	// there; the backend would refuse the edit anyway).
	if (!vm.scheduling?.custom) {
		const origGroups = new Map((vm.scheduling?.groups ?? []).map((g) => [g.name, g]));
		const addGroups = form.groups
			.filter((g) => !g.removed && g.name.trim())
			.filter((g) => {
				const o = origGroups.get(g.name);
				return !o || o.mode !== g.mode || !!o.strict !== !!g.strict;
			})
			.map((g) => ({ name: g.name.trim(), mode: g.mode, strict: g.strict || undefined }));
		if (addGroups.length) req.addGroups = addGroups;
		const removeGroups = form.groups.filter((g) => !g.isNew && g.removed).map((g) => g.name);
		if (removeGroups.length) req.removeGroups = removeGroups;
		const origPin = vm.scheduling?.pin ?? [];
		const pin = form.pin.map((h) => h.trim()).filter(Boolean);
		if (pin.join('\n') !== origPin.join('\n')) req.pin = pin;
	}

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

// Human-readable summary of exactly what will be staged, derived from the same
// request the backend receives — so the review never diverges from the commit.
export function summarize(vm: VM, form: EditForm): { label: string; value: string }[] {
	const r = buildEditRequest(vm, form);
	const out: { label: string; value: string }[] = [];
	if (r.power) out.push({ label: 'Power', value: `${vm.power} → ${r.power}` });
	if (r.sizing === 'instancetype')
		out.push({
			label: 'Sizing',
			value: `Instance type · ${r.instancetype ?? form.instancetype}`,
		});
	if (r.sizing === 'custom')
		out.push({ label: 'Sizing', value: `Custom · ${r.cpuCores} CPU / ${r.memory}` });
	if (r.preference) out.push({ label: 'Preference', value: r.preference });
	if (r.drsExclude !== undefined)
		out.push({
			label: 'Dynamic Rescheduling',
			value: r.drsExclude ? 'excluded from rebalancing' : 'rebalanced',
		});
	if (r.evictionStrategy !== undefined)
		out.push({ label: 'Eviction strategy', value: r.evictionStrategy || 'cluster default' });
	for (const [k, v] of Object.entries(r.setLabels ?? {}))
		out.push({ label: `Label ${k}`, value: v });
	for (const k of r.removeLabels ?? []) out.push({ label: `Label ${k}`, value: 'removed' });
	for (const d of r.addDisks ?? [])
		out.push({
			label: 'Disk added',
			value: `${d.name} (${d.size}${d.storageClass ? `, ${d.storageClass}` : ''})`,
		});
	for (const n of r.removeDisks ?? []) out.push({ label: 'Disk removed', value: n });
	for (const n of r.addNetworks ?? []) out.push({ label: 'Adapter added', value: n.name });
	for (const n of r.removeNetworks ?? []) out.push({ label: 'Adapter removed', value: n });
	for (const g of r.addGroups ?? [])
		out.push({
			label: `Group ${g.name}`,
			value: `keep ${g.mode}${g.strict ? ', strict' : ', preferred'}`,
		});
	for (const n of r.removeGroups ?? []) out.push({ label: `Group ${n}`, value: 'removed' });
	if (r.pin)
		out.push({ label: 'Host pinning', value: r.pin.length ? r.pin.join(', ') : 'removed' });
	return out;
}
