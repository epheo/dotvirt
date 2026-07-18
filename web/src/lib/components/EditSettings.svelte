<script lang="ts">
	import { X } from 'lucide-svelte';
	import { untrack } from 'svelte';
	import {
		api,
		Unauthorized,
		type Network,
		type NodeTarget,
		type Options,
		type VM,
	} from '$lib/api';
	import { buildEditRequest, seedEditForm } from '$lib/editform';
	import { kindLabel, attachableNetworks, attachRef } from '$lib/networks';
	import { validName, NAME_HINT } from '$lib/validate';
	import CheckGroup from './CheckGroup.svelte';
	import Wizard from './Wizard.svelte';
	import FormField from './FormField.svelte';
	import SelectInput from './SelectInput.svelte';
	import TextInput from './TextInput.svelte';

	let {
		vm,
		networks = [],
		onclose,
		onstaged,
		initialSection,
	}: {
		vm: VM;
		networks?: Network[]; // port-group catalog (from the page), for the adapter picker
		onclose: () => void;
		onstaged: () => void;
		// Opened from a Configure section: start the wizard on that step.
		initialSection?: 'compute' | 'scheduling' | 'storage' | 'network' | 'labels';
	} = $props();

	let options = $state<Options | null>(null);

	// The modal is mounted fresh per VM, so capturing the initial prop value to
	// seed the editable working copy is intentional.
	// svelte-ignore state_referenced_locally
	let form = $state(seedEditForm(vm));

	let saving = $state(false);
	let error = $state('');

	// Start on the step the user opened from a Configure section (else Compute).
	// svelte-ignore state_referenced_locally
	let current = $state(
		{ compute: 0, scheduling: 1, storage: 2, network: 3, labels: 4 }[initialSection ?? 'compute'] ??
			0,
	);

	// Hosts for the pin picker. Listing nodes is cluster-scoped RBAC; without it
	// the picker degrades to a free-text host list.
	let nodes = $state<NodeTarget[] | null>(null);
	let canPickHosts = $state(true);
	$effect(() => {
		untrack(() =>
			api
				.nodes()
				.then((n) => (nodes = n))
				.catch((e) => {
					if (e instanceof Unauthorized) return;
					canPickHosts = false;
					nodes = [];
				}),
		);
	});
	// Offer every schedulable-ish host, plus any already-pinned name that no
	// longer exists (so a stale pin can still be unchecked).
	const hostItems = $derived.by(() => {
		const names = new Set((nodes ?? []).map((n) => n.name));
		for (const h of form.pin) names.add(h);
		return [...names].sort().map((n) => {
			const node = (nodes ?? []).find((x) => x.name === n);
			return {
				value: n,
				hint: !node
					? 'unknown'
					: node.unschedulable
						? 'cordoned'
						: node.maintenance
							? 'maintenance'
							: '',
			};
		});
	});
	let pinText = $state('');
	// svelte-ignore state_referenced_locally
	pinText = form.pin.join(' ');
	function syncPinText() {
		form.pin = pinText.split(/[\s,]+/).filter(Boolean);
	}

	const customScheduling = $derived(!!vm.scheduling?.custom);

	function addGroup() {
		form.groups = [
			...form.groups,
			{ name: '', mode: 'together', strict: true, removed: false, isNew: true },
		];
	}

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
	const selectedIT = $derived(
		(options?.instancetypes ?? []).find((i) => i.name === form.instancetype),
	);

	function setMode(next: 'instancetype' | 'custom') {
		if (next === form.mode) return;
		// Converting to custom: start from the instancetype's numbers so the user
		// tweaks real values rather than the blank inline fields.
		if (next === 'custom' && !form.cpuCores && !form.memory && selectedIT) {
			form.cpuCores = selectedIT.cpu;
			form.memory = selectedIT.memory;
		}
		form.mode = next;
	}

	function addNewDevice(kind: 'disk' | 'network') {
		if (kind === 'disk') {
			form.disks = [
				...form.disks,
				{
					name: `disk-${form.disks.length + 1}`,
					type: 'dataVolume',
					size: '10Gi',
					storageClass: '',
					removed: false,
					isNew: true,
				},
			];
		} else {
			const first = available[0];
			form.nics = [
				...form.nics,
				{
					name: first ? first.name : 'net1',
					network: first ? attachRef(first) : '',
					removed: false,
					isNew: true,
				},
			];
		}
	}

	const dirty = $derived.by(() => {
		const r = buildEditRequest(vm, form);
		// Anything beyond the always-present sourceFile means a real change was made.
		return Object.keys(r).length > 1;
	});

	// Compute step is flagged invalid only when custom sizing is half-filled (the
	// one combination that would silently drop the sizing change). Other states are
	// "optional" (no marker) since every edit field is optional.
	const computeValid = $derived(
		form.mode === 'custom' && !(form.cpuCores && form.memory) ? false : undefined,
	);

	// Human-readable summary of exactly what will be staged, derived from the same
	// request the backend receives — so the review never diverges from the commit.
	const summary = $derived.by(() => {
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
			out.push({ label: 'DRS', value: r.drsExclude ? 'excluded from rebalancing' : 'rebalanced' });
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
	});

	async function stage() {
		if (!dirty) return;
		saving = true;
		error = '';
		try {
			await api.stageEdit(vm.namespace, vm.name, buildEditRequest(vm, form));
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
		<div
			class="mb-3 rounded border border-warn-soft bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink"
		>
			{optionsError} — the instance type / preference dropdowns may be empty.
		</div>
	{/if}

	<!-- Sizing mode: CPU/memory OR an instance type, never both -->
	<div class="mb-3">
		<span class="text-ink-muted">Sizing</span>
		<div class="mt-1 inline-flex overflow-hidden rounded border border-line-strong">
			<button
				type="button"
				onclick={() => setMode('instancetype')}
				class="px-3 py-1 text-xs {form.mode === 'instancetype'
					? 'bg-accent text-white'
					: 'bg-panel text-ink-soft hover:bg-inset'}"
			>
				Instance type
			</button>
			<button
				type="button"
				onclick={() => setMode('custom')}
				class="border-l border-line-strong px-3 py-1 text-xs {form.mode === 'custom'
					? 'bg-accent text-white'
					: 'bg-panel text-ink-soft hover:bg-inset'}"
			>
				Custom CPU/Memory
			</button>
		</div>
		{#if form.mode !== (vm.instancetype ? 'instancetype' : 'custom')}
			<p class="mt-1 text-xs text-warn-ink">
				{form.mode === 'custom'
					? 'Switches to custom CPU/memory and removes the instance type.'
					: 'Uses the instance type and removes the custom CPU/memory.'}
			</p>
		{/if}
	</div>

	<div class="grid grid-cols-2 gap-3">
		<FormField label="Power state">
			<select bind:value={form.power} class="w-full rounded border border-line-strong px-2 py-1">
				<option value="On">On</option>
				<option value="Off">Off</option>
				{#if form.power === 'Unknown'}<option value="Unknown">Unknown</option>{/if}
			</select>
		</FormField>

		{#if form.mode === 'instancetype'}
			<FormField label="Instance type">
				<select
					bind:value={form.instancetype}
					class="w-full rounded border border-line-strong px-2 py-1"
				>
					<!-- Keep the current value selectable even if it isn't in the cluster
					     list (orphaned ref, or options still loading) so the binding can't
					     silently desync to the first option. -->
					{#if form.instancetype && !selectedIT}
						<option value={form.instancetype}
							>{form.instancetype} (current — not in cluster list)</option
						>
					{/if}
					{#each options?.instancetypes ?? [] as it (it.name)}
						<option value={it.name}>{it.name} ({it.cpu} CPU / {it.memory})</option>
					{/each}
				</select>
			</FormField>
			<FormField label="Preference">
				<select
					bind:value={form.preference}
					class="w-full rounded border border-line-strong px-2 py-1"
				>
					<option value="">— unchanged —</option>
					{#each options?.preferences ?? [] as p (p.name)}
						<option value={p.name}>{p.displayName || p.name}</option>
					{/each}
				</select>
			</FormField>
			<p class="col-span-2 text-xs text-ink-faint">
				CPU and memory are provided by the instance type{selectedIT
					? `: ${selectedIT.cpu} CPU / ${selectedIT.memory}`
					: ''}.
			</p>
		{:else}
			<FormField label="CPU cores">
				<input
					type="number"
					min="1"
					bind:value={form.cpuCores}
					class="w-full rounded border border-line-strong px-2 py-1"
				/>
			</FormField>
			<FormField label="Memory">
				<input
					bind:value={form.memory}
					placeholder="2Gi"
					class="w-full rounded border border-line-strong px-2 py-1"
				/>
			</FormField>
			<FormField label="Preference">
				<select
					bind:value={form.preference}
					class="w-full rounded border border-line-strong px-2 py-1"
				>
					<option value="">— unchanged —</option>
					{#each options?.preferences ?? [] as p (p.name)}
						<option value={p.name}>{p.displayName || p.name}</option>
					{/each}
				</select>
			</FormField>
			{#if !(form.cpuCores && form.memory)}
				<p class="col-span-2 text-xs text-warn-ink">
					Set both CPU cores and memory to apply custom sizing.
				</p>
			{/if}
		{/if}
	</div>
{/snippet}

{#snippet stepScheduling()}
	<div class="space-y-4">
		<!-- Placement groups: vCenter's DRS rules. Membership + rule are one
		     encoding on the manifest, so this edits both at once. -->
		<div>
			<div class="mb-1 flex items-center justify-between">
				<span class="text-ink-muted">Placement groups (DRS rules)</span>
				{#if !customScheduling}
					<button onclick={addGroup} type="button" class="text-xs text-accent hover:underline"
						>+ Add group</button
					>
				{/if}
			</div>
			{#if customScheduling}
				<p class="rounded border border-warn-soft bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink">
					This VM carries hand-written affinity or node selection — placement is managed in git, not
					from this form.
				</p>
			{:else}
				{#each form.groups as group, i (i)}
					<div
						class="mb-1 flex flex-wrap items-center gap-2 {group.removed
							? 'opacity-40 line-through'
							: ''}"
					>
						{#if group.isNew}
							<TextInput
								bind:value={group.name}
								placeholder="web-tier"
								mono
								class="w-40 flex-none"
							/>
						{:else}
							<span class="w-40 truncate font-mono text-[13px] text-ink">{group.name}</span>
						{/if}
						<SelectInput bind:value={group.mode} class="w-56 flex-none" disabled={group.removed}>
							<option value="together">Keep together — same host</option>
							<option value="apart">Keep apart — different hosts</option>
						</SelectInput>
						<label class="flex items-center gap-1.5 text-xs text-ink-soft">
							<input type="checkbox" bind:checked={group.strict} disabled={group.removed} />
							strict
						</label>
						<button
							onclick={() =>
								group.isNew
									? (form.groups = form.groups.filter((_, idx) => idx !== i))
									: (group.removed = !group.removed)}
							class="ml-auto text-xs {group.removed ? 'text-accent' : 'text-danger'}"
						>
							{group.removed ? 'undo' : 'remove'}
						</button>
						{#if group.isNew && group.name && !validName(group.name)}
							<p class="w-full text-xs text-warn-ink">{NAME_HINT}</p>
						{/if}
					</div>
				{:else}
					<p class="text-xs text-ink-faint">
						No groups. VMs sharing a group are kept on one host (together) or spread across hosts
						(apart) — strict rules bind the scheduler, non-strict are best effort.
					</p>
				{/each}
			{/if}
		</div>

		<!-- Host pinning: a required node-affinity host list. -->
		{#if !customScheduling}
			<div>
				<span class="mb-1 block text-ink-muted">Pin to hosts</span>
				{#if nodes === null && canPickHosts}
					<p class="text-xs text-ink-faint">Loading hosts…</p>
				{:else if canPickHosts && hostItems.length}
					<CheckGroup items={hostItems} bind:selected={form.pin} />
				{:else}
					<TextInput
						bind:value={pinText}
						oninput={syncPinText}
						placeholder="w1 w2 (space separated; empty = any host)"
						mono
					/>
				{/if}
				<p class="mt-1 text-xs text-ink-faint">
					{form.pin.length
						? 'The scheduler may only place this VM on the checked hosts.'
						: 'No pinning — the scheduler places this VM on any eligible host.'}
				</p>
			</div>
		{/if}

		<!-- DRS participation + eviction behavior (vCenter's per-VM automation
		     override). -->
		<div class="border-t border-line-soft pt-3">
			<label class="flex items-start gap-2 text-[13px]">
				<input type="checkbox" bind:checked={form.drsExclude} class="mt-0.5" />
				<span>
					Exclude from DRS load balancing
					<span class="block text-xs text-ink-faint">
						Automatic rebalancing skips this VM; node drains still live-migrate it.
					</span>
				</span>
			</label>
			<FormField label="Eviction strategy">
				<SelectInput bind:value={form.evictionStrategy}>
					<option value="">Cluster default</option>
					<option value="LiveMigrate">LiveMigrate — evictions live-migrate the VM</option>
					<option value="LiveMigrateIfPossible"
						>LiveMigrateIfPossible — migrate when possible, else restart</option
					>
					<option value="None">None — pinned (blocks node drains)</option>
				</SelectInput>
			</FormField>
		</div>
	</div>
{/snippet}

{#snippet stepStorage()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-ink-faint">{form.disks.filter((d) => !d.removed).length} disk(s)</span
		>
		<button
			onclick={() => addNewDevice('disk')}
			class="rounded border border-line-strong px-2 py-0.5 text-xs hover:bg-inset"
			>+ Add hard disk</button
		>
	</div>
	{#each form.disks as disk, i (i)}
		<div class="mb-1 flex items-center gap-2 {disk.removed ? 'opacity-40 line-through' : ''}">
			<span class="w-32 truncate text-ink-soft">Hard disk {i + 1}</span>
			{#if disk.isNew}
				<input
					bind:value={disk.name}
					class="w-24 rounded border border-line-strong px-2 py-0.5 text-xs"
				/>
				<input
					bind:value={disk.size}
					class="w-16 rounded border border-line-strong px-2 py-0.5 text-xs"
				/>
				<select
					bind:value={disk.storageClass}
					class="min-w-0 flex-1 rounded border border-line-strong px-2 py-0.5 text-xs"
				>
					<option value="">cluster default</option>
					{#each options?.storageClasses ?? [] as sc (sc.name)}
						<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
					{/each}
				</select>
			{:else}
				<span class="text-xs text-ink-muted"
					>{disk.name} ({disk.type}{disk.size ? ` · ${disk.size}` : ''}{disk.storageClass
						? ` · ${disk.storageClass}`
						: ''})</span
				>
			{/if}
			<button
				onclick={() => (disk.removed = !disk.removed)}
				class="ml-auto text-xs {disk.removed ? 'text-accent' : 'text-danger'}"
			>
				{disk.removed ? 'undo' : 'remove'}
			</button>
		</div>
	{/each}
	{#if form.disks.filter((d) => !d.removed).length === 0}<p class="text-xs text-ink-faint">
			No disks.
		</p>{/if}
{/snippet}

{#snippet stepNetworks()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-ink-faint"
			>{form.nics.filter((n) => !n.removed).length} adapter(s)</span
		>
		<button
			onclick={() => addNewDevice('network')}
			class="rounded border border-line-strong px-2 py-0.5 text-xs hover:bg-inset"
			>+ Add network adapter</button
		>
	</div>
	{#each form.nics as nic, i (i)}
		<div class="mb-1 flex items-center gap-2 {nic.removed ? 'opacity-40 line-through' : ''}">
			<span class="w-32 truncate text-ink-soft">Network adapter {i + 1}</span>
			{#if nic.isNew}
				<select
					bind:value={nic.network}
					class="w-60 rounded border border-line-strong px-2 py-0.5 text-xs"
				>
					{#each available as net (net.scope + (net.namespace ?? '') + net.name)}
						<option value={attachRef(net)}
							>{net.name} — {kindLabel(net.kind)}{net.scope === 'shared' ? ' · shared' : ''}</option
						>
					{/each}
				</select>
			{:else}
				<span class="text-xs text-ink-muted">{nic.name} ({nic.network})</span>
			{/if}
			<button
				onclick={() => (nic.removed = !nic.removed)}
				class="ml-auto text-xs {nic.removed ? 'text-accent' : 'text-danger'}"
			>
				{nic.removed ? 'undo' : 'remove'}
			</button>
		</div>
	{/each}
	{#if form.nics.filter((n) => !n.removed).length === 0}<p class="text-xs text-ink-faint">
			No adapters.
		</p>{/if}
{/snippet}

{#snippet stepLabels()}
	<div class="mb-2 flex items-center justify-between">
		<span class="text-xs text-ink-faint">Key/value metadata.</span>
		<button
			onclick={() => (form.labelRows = [...form.labelRows, { key: '', value: '' }])}
			class="text-xs text-accent hover:underline">+ Add label</button
		>
	</div>
	{#each form.labelRows as row, i (i)}
		<div class="mb-1 flex gap-2">
			<input
				bind:value={row.key}
				placeholder="key"
				class="w-1/2 rounded border border-line-strong px-2 py-0.5 text-xs"
			/>
			<input
				bind:value={row.value}
				placeholder="value"
				class="w-1/2 rounded border border-line-strong px-2 py-0.5 text-xs"
			/>
			<button
				onclick={() => (form.labelRows = form.labelRows.filter((_, idx) => idx !== i))}
				aria-label="Remove label"
				class="text-danger"><X size={14} /></button
			>
		</div>
	{/each}
	{#if form.labelRows.length === 0}<p class="text-xs text-ink-faint">No labels.</p>{/if}
{/snippet}

{#snippet review()}
	<p class="mb-3 text-xs text-ink-muted">
		Review the staged changes, then stage them into the changeset.
	</p>
	{#if summary.length === 0}
		<div class="rounded border border-line bg-inset p-3 text-xs text-ink-muted">
			No changes yet — adjust a setting in an earlier step.
		</div>
	{:else}
		<dl class="divide-y divide-line-soft rounded border border-line text-[13px]">
			{#each summary as c, i (i)}
				<div class="flex justify-between gap-3 px-3 py-1.5">
					<dt class="shrink-0 text-ink-muted">{c.label}</dt>
					<dd class="min-w-0 truncate text-right text-ink-soft">{c.value}</dd>
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
		{ title: 'Scheduling', body: stepScheduling },
		{ title: 'Storage', body: stepStorage },
		{ title: 'Networks', body: stepNetworks },
		{ title: 'Labels', body: stepLabels },
		{ title: 'Ready to complete', body: review },
	]}
	canFinish={dirty}
	submitting={saving}
	{error}
	finishLabel="Stage change"
	footerHint="Changes are staged into the changeset; review &amp; open a PR from “Changes”."
	onfinish={stage}
	{onclose}
/>
