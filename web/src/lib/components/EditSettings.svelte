<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type EditRequest, type Network, type Options, type VM } from '$lib/api';
	import { kindLabel, attachableNetworks, attachRef } from '$lib/networks';
	import Wizard from './Wizard.svelte';

	let {
		vm,
		networks = [],
		onclose,
		onstaged,
		initialSection
	}: {
		vm: VM;
		networks?: Network[]; // port-group catalog (from the page), for the adapter picker
		onclose: () => void;
		onstaged: () => void;
		// Opened from a Configure section: start the wizard on that step.
		initialSection?: 'compute' | 'storage' | 'network' | 'labels';
	} = $props();

	let options = $state<Options | null>(null);

	// The modal is mounted fresh per VM, so capturing the initial prop value to
	// seed the editable working copy is intentional.
	// svelte-ignore state_referenced_locally
	const seed = vm;
	let power = $state(seed.power);
	let cpuCores = $state<number | undefined>(seed.cpuCores);
	let memory = $state(seed.memory ?? '');
	let instancetype = $state(seed.instancetype ?? '');
	let preference = $state(seed.preference ?? '');
	// Sizing is one of two mutually-exclusive modes — KubeVirt forbids a VM from
	// carrying both an instancetype and inline cpu/memory. Seed from how the VM is
	// sized today; the toggle lets the user convert between them.
	// svelte-ignore state_referenced_locally
	let mode = $state<'instancetype' | 'custom'>(seed.instancetype ? 'instancetype' : 'custom');
	let labelRows = $state(Object.entries(seed.labels ?? {}).map(([key, value]) => ({ key, value })));

	// Disks: existing (with a removed flag) + newly added blank disks.
	let disks = $state((seed.disks ?? []).map((d) => ({ ...d, removed: false, isNew: false })));
	let nics = $state((seed.networks ?? []).map((n) => ({ ...n, removed: false, isNew: false })));

	let saving = $state(false);
	let error = $state('');

	// Start on the step the user opened from a Configure section (else Compute).
	// svelte-ignore state_referenced_locally
	let current = $state({ compute: 0, storage: 1, network: 2, labels: 3 }[initialSection ?? 'compute'] ?? 0);

	let optionsError = $state('');
	$effect(() => {
		api
			.options()
			.then((o) => (options = o))
			.catch((e) => (optionsError = `Couldn't load cluster options: ${e}`));
	});

	// Attachable secondaries for this VM's namespace = shared (CUDN) networks +
	// this namespace's own non-default networks (the primary "VM Network" backs the
	// default NIC, so it isn't an add-able adapter).
	const available = $derived(attachableNetworks(networks, vm.namespace));

	// The selected instancetype's own CPU/memory — for the read-only hint and to
	// seed the custom inputs when converting an instancetype VM to custom sizing.
	const selectedIT = $derived((options?.instancetypes ?? []).find((i) => i.name === instancetype));

	function setMode(next: 'instancetype' | 'custom') {
		if (next === mode) return;
		// Converting to custom: start from the instancetype's numbers so the user
		// tweaks real values rather than the blank inline fields.
		if (next === 'custom' && !cpuCores && !memory && selectedIT) {
			cpuCores = selectedIT.cpu;
			memory = selectedIT.memory;
		}
		mode = next;
	}

	function addNewDevice(kind: 'disk' | 'network') {
		if (kind === 'disk') {
			disks = [...disks, { name: `disk-${disks.length + 1}`, type: 'emptyDisk', size: '10Gi', removed: false, isNew: true }];
		} else {
			const first = available[0];
			nics = [...nics, { name: first ? first.name : 'net1', network: first ? attachRef(first) : '', removed: false, isNew: true }];
		}
	}

	function buildRequest(): EditRequest {
		const req: EditRequest = { sourceFile: vm.sourceFile };
		if (power !== vm.power) req.power = power;

		// CPU/memory and instance type are mutually exclusive (KubeVirt forbids both),
		// so send only the active mode's fields and tell the backend which to keep.
		const vmMode = vm.instancetype ? 'instancetype' : 'custom';
		const sizingChanged =
			mode !== vmMode ||
			(mode === 'custom' && (cpuCores !== vm.cpuCores || memory !== (vm.memory ?? ''))) ||
			(mode === 'instancetype' && instancetype !== (vm.instancetype ?? ''));
		// A VM wrongly carrying inline cpu/memory under an instancetype must be
		// normalized even when nothing visibly changed — this heals a SyncFailed VM.
		const needsHeal = mode === 'instancetype' && (!!vm.cpuCores || !!vm.memory);
		if (sizingChanged || needsHeal) {
			if (mode === 'custom') {
				// Only convert to custom sizing when BOTH values are present. Sending
				// sizing:'custom' strips the instance type on the backend; without
				// replacement cpu/memory that leaves the VM unsized and the KubeVirt
				// webhook rejects it (re-creating the SyncFailed state this dialog fixes).
				if (cpuCores && memory) {
					req.sizing = 'custom';
					req.cpuCores = cpuCores;
					req.memory = memory;
				}
			} else if (instancetype) {
				req.sizing = 'instancetype';
				req.instancetype = instancetype;
			}
		}
		if (preference && preference !== (vm.preference ?? '')) req.preference = preference;

		// Labels: upsert any changed/new, remove any deleted-from-original.
		const set: Record<string, string> = {};
		for (const r of labelRows) if (r.key.trim()) set[r.key.trim()] = r.value;
		const original = vm.labels ?? {};
		const setChanged: Record<string, string> = {};
		for (const [k, v] of Object.entries(set)) if (original[k] !== v) setChanged[k] = v;
		if (Object.keys(setChanged).length) req.setLabels = setChanged;
		const removedLabels = Object.keys(original).filter((k) => !(k in set));
		if (removedLabels.length) req.removeLabels = removedLabels;

		// Disks
		const addDisks = disks.filter((d) => d.isNew && !d.removed && d.name.trim());
		if (addDisks.length) req.addDisks = addDisks.map((d) => ({ name: d.name, size: d.size ?? '10Gi' }));
		const removeDisks = disks.filter((d) => !d.isNew && d.removed).map((d) => d.name);
		if (removeDisks.length) req.removeDisks = removeDisks;

		// Networks
		const addNetworks = nics.filter((n) => n.isNew && !n.removed && n.network);
		if (addNetworks.length) req.addNetworks = addNetworks.map((n) => ({ name: n.network! }));
		const removeNetworks = nics.filter((n) => !n.isNew && n.removed).map((n) => n.name);
		if (removeNetworks.length) req.removeNetworks = removeNetworks;

		return req;
	}

	const dirty = $derived.by(() => {
		const r = buildRequest();
		// Anything beyond the always-present sourceFile means a real change was made.
		return Object.keys(r).length > 1;
	});

	// Compute step is flagged invalid only when custom sizing is half-filled (the
	// one combination that would silently drop the sizing change). Other states are
	// "optional" (no marker) since every edit field is optional.
	const computeValid = $derived(mode === 'custom' && !(cpuCores && memory) ? false : undefined);

	// Human-readable summary of exactly what will be staged, derived from the same
	// request the backend receives — so the review never diverges from the commit.
	const summary = $derived.by(() => {
		const r = buildRequest();
		const out: { label: string; value: string }[] = [];
		if (r.power) out.push({ label: 'Power', value: `${vm.power} → ${r.power}` });
		if (r.sizing === 'instancetype')
			out.push({ label: 'Sizing', value: `Instance type · ${r.instancetype ?? instancetype}` });
		if (r.sizing === 'custom') out.push({ label: 'Sizing', value: `Custom · ${r.cpuCores} CPU / ${r.memory}` });
		if (r.preference) out.push({ label: 'Preference', value: r.preference });
		for (const [k, v] of Object.entries(r.setLabels ?? {})) out.push({ label: `Label ${k}`, value: v });
		for (const k of r.removeLabels ?? []) out.push({ label: `Label ${k}`, value: 'removed' });
		for (const d of r.addDisks ?? []) out.push({ label: 'Disk added', value: `${d.name} (${d.size})` });
		for (const n of r.removeDisks ?? []) out.push({ label: 'Disk removed', value: n });
		for (const n of r.addNetworks ?? []) out.push({ label: 'Adapter added', value: n.name });
		for (const n of r.removeNetworks ?? []) out.push({ label: 'Adapter removed', value: n });
		return out;
	});

	async function stage() {
		if (!dirty) return;
		saving = true;
		error = '';
		try {
			await api.stageEdit(vm.namespace, vm.name, buildRequest());
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			saving = false;
		}
	}
</script>

{#snippet stepCompute()}
	{#if optionsError}
		<div class="mb-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
			{optionsError} — the instance type / preference dropdowns may be empty.
		</div>
	{/if}

	<!-- Sizing mode: CPU/memory OR an instance type, never both -->
	<div class="mb-3">
		<span class="text-slate-500">Sizing</span>
		<div class="mt-1 inline-flex overflow-hidden rounded border border-slate-300">
			<button
				type="button"
				onclick={() => setMode('instancetype')}
				class="px-3 py-1 text-xs {mode === 'instancetype' ? 'bg-blue-600 text-white' : 'bg-white text-slate-600 hover:bg-slate-50'}"
			>
				Instance type
			</button>
			<button
				type="button"
				onclick={() => setMode('custom')}
				class="border-l border-slate-300 px-3 py-1 text-xs {mode === 'custom' ? 'bg-blue-600 text-white' : 'bg-white text-slate-600 hover:bg-slate-50'}"
			>
				Custom CPU/Memory
			</button>
		</div>
		{#if mode !== (vm.instancetype ? 'instancetype' : 'custom')}
			<p class="mt-1 text-xs text-amber-700">
				{mode === 'custom'
					? 'Switches to custom CPU/memory and removes the instance type.'
					: 'Uses the instance type and removes the custom CPU/memory.'}
			</p>
		{/if}
	</div>

	<div class="grid grid-cols-2 gap-3">
		<label class="block">
			<span class="text-slate-500">Power state</span>
			<select bind:value={power} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
				<option value="On">On</option>
				<option value="Off">Off</option>
				{#if power === 'Unknown'}<option value="Unknown">Unknown</option>{/if}
			</select>
		</label>

		{#if mode === 'instancetype'}
			<label class="block">
				<span class="text-slate-500">Instance type</span>
				<select bind:value={instancetype} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
					<!-- Keep the current value selectable even if it isn't in the cluster
					     list (orphaned ref, or options still loading) so the binding can't
					     silently desync to the first option. -->
					{#if instancetype && !selectedIT}
						<option value={instancetype}>{instancetype} (current — not in cluster list)</option>
					{/if}
					{#each options?.instancetypes ?? [] as it (it.name)}
						<option value={it.name}>{it.name} ({it.cpu} CPU / {it.memory})</option>
					{/each}
				</select>
			</label>
			<label class="block">
				<span class="text-slate-500">Preference</span>
				<select bind:value={preference} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
					<option value="">— unchanged —</option>
					{#each options?.preferences ?? [] as p (p.name)}
						<option value={p.name}>{p.displayName || p.name}</option>
					{/each}
				</select>
			</label>
			<p class="col-span-2 text-xs text-slate-400">
				CPU and memory are provided by the instance type{selectedIT ? `: ${selectedIT.cpu} CPU / ${selectedIT.memory}` : ''}.
			</p>
		{:else}
			<label class="block">
				<span class="text-slate-500">CPU cores</span>
				<input type="number" min="1" bind:value={cpuCores} class="mt-1 w-full rounded border border-slate-300 px-2 py-1" />
			</label>
			<label class="block">
				<span class="text-slate-500">Memory</span>
				<input bind:value={memory} placeholder="2Gi" class="mt-1 w-full rounded border border-slate-300 px-2 py-1" />
			</label>
			<label class="block">
				<span class="text-slate-500">Preference</span>
				<select bind:value={preference} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
					<option value="">— unchanged —</option>
					{#each options?.preferences ?? [] as p (p.name)}
						<option value={p.name}>{p.displayName || p.name}</option>
					{/each}
				</select>
			</label>
			{#if !(cpuCores && memory)}
				<p class="col-span-2 text-xs text-amber-700">Set both CPU cores and memory to apply custom sizing.</p>
			{/if}
		{/if}
	</div>
{/snippet}

{#snippet stepStorage()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-slate-400">{disks.filter((d) => !d.removed).length} disk(s)</span>
		<button onclick={() => addNewDevice('disk')} class="rounded border border-slate-300 px-2 py-0.5 text-xs hover:bg-slate-50">+ Add hard disk</button>
	</div>
	{#each disks as disk, i (i)}
		<div class="mb-1 flex items-center gap-2 {disk.removed ? 'opacity-40 line-through' : ''}">
			<span class="w-32 truncate text-slate-700">Hard disk {i + 1}</span>
			{#if disk.isNew}
				<input bind:value={disk.name} class="w-28 rounded border border-slate-300 px-2 py-0.5 text-xs" />
				<input bind:value={disk.size} class="w-20 rounded border border-slate-300 px-2 py-0.5 text-xs" />
			{:else}
				<span class="text-xs text-slate-500">{disk.name} ({disk.type}{disk.size ? ` · ${disk.size}` : ''})</span>
			{/if}
			<button onclick={() => (disk.removed = !disk.removed)} class="ml-auto text-xs {disk.removed ? 'text-blue-600' : 'text-red-500'}">
				{disk.removed ? 'undo' : 'remove'}
			</button>
		</div>
	{/each}
	{#if disks.filter((d) => !d.removed).length === 0}<p class="text-xs text-slate-400">No disks.</p>{/if}
{/snippet}

{#snippet stepNetworks()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-slate-400">{nics.filter((n) => !n.removed).length} adapter(s)</span>
		<button onclick={() => addNewDevice('network')} class="rounded border border-slate-300 px-2 py-0.5 text-xs hover:bg-slate-50">+ Add network adapter</button>
	</div>
	{#each nics as nic, i (i)}
		<div class="mb-1 flex items-center gap-2 {nic.removed ? 'opacity-40 line-through' : ''}">
			<span class="w-32 truncate text-slate-700">Network adapter {i + 1}</span>
			{#if nic.isNew}
				<select bind:value={nic.network} class="w-60 rounded border border-slate-300 px-2 py-0.5 text-xs">
					{#each available as net (net.scope + (net.namespace ?? '') + net.name)}
						<option value={attachRef(net)}>{net.name} — {kindLabel(net.kind)}{net.scope === 'shared' ? ' · shared' : ''}</option>
					{/each}
				</select>
			{:else}
				<span class="text-xs text-slate-500">{nic.name} ({nic.network})</span>
			{/if}
			<button onclick={() => (nic.removed = !nic.removed)} class="ml-auto text-xs {nic.removed ? 'text-blue-600' : 'text-red-500'}">
				{nic.removed ? 'undo' : 'remove'}
			</button>
		</div>
	{/each}
	{#if nics.filter((n) => !n.removed).length === 0}<p class="text-xs text-slate-400">No adapters.</p>{/if}
{/snippet}

{#snippet stepLabels()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-slate-400">Key/value metadata.</span>
		<button onclick={() => (labelRows = [...labelRows, { key: '', value: '' }])} class="text-xs text-blue-600 hover:underline">+ Add label</button>
	</div>
	{#each labelRows as row, i (i)}
		<div class="mb-1 flex gap-2">
			<input bind:value={row.key} placeholder="key" class="w-1/2 rounded border border-slate-300 px-2 py-0.5 text-xs" />
			<input bind:value={row.value} placeholder="value" class="w-1/2 rounded border border-slate-300 px-2 py-0.5 text-xs" />
			<button onclick={() => (labelRows = labelRows.filter((_, idx) => idx !== i))} aria-label="Remove label" class="text-red-500"><X size={14} /></button>
		</div>
	{/each}
	{#if labelRows.length === 0}<p class="text-xs text-slate-400">No labels.</p>{/if}
{/snippet}

{#snippet review()}
	<p class="mb-3 text-xs text-slate-500">Review the staged changes, then stage them into the changeset.</p>
	{#if summary.length === 0}
		<div class="rounded border border-slate-200 bg-slate-50 p-3 text-xs text-slate-500">
			No changes yet — adjust a setting in an earlier step.
		</div>
	{:else}
		<dl class="divide-y divide-slate-100 rounded border border-slate-200 text-[13px]">
			{#each summary as c, i (i)}
				<div class="flex justify-between gap-3 px-3 py-1.5">
					<dt class="shrink-0 text-slate-500">{c.label}</dt>
					<dd class="min-w-0 truncate text-right text-slate-700">{c.value}</dd>
				</div>
			{/each}
		</dl>
	{/if}
{/snippet}

<Wizard
	title={`Edit Settings — ${vm.name}`}
	bind:current
	steps={[
		{ title: 'Compute', valid: computeValid, body: stepCompute },
		{ title: 'Storage', body: stepStorage },
		{ title: 'Networks', body: stepNetworks },
		{ title: 'Labels', body: stepLabels },
		{ title: 'Ready to complete', body: review }
	]}
	canFinish={dirty}
	submitting={saving}
	{error}
	finishLabel="Stage change"
	footerHint="Changes are staged into the changeset; review &amp; open a PR from “Changes”."
	onfinish={stage}
	{onclose}
/>
