<script lang="ts">
	import { ChevronDown, ChevronUp } from 'lucide-svelte';
	import { vmKey, type DraftItem, type Power, type SyncStatus, type VM } from '$lib/api';
	import { quantityBytes } from '$lib/format';
	import { vmSizing } from '$lib/sizing';
	import { phaseTone } from '$lib/status';
	import { inventory } from '$lib/state/inventory.svelte';
	import { persisted } from '$lib/state/persisted.svelte';
	import { TBODY, THEAD_TR } from '$lib/table';
	import PowerDot from './PowerDot.svelte';
	import { vmHref } from '$lib/nav';
	import SelectInput from './SelectInput.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import StatusPill from './StatusPill.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import TextInput from './TextInput.svelte';

	let {
		vms,
		onselect,
		selected = $bindable(new Set<string>()),
		staged,
		onstagedopen,
		oncontextvm,
		activeKey = null,
	}: {
		vms: VM[];
		onselect: (vm: VM) => void;
		selected?: Set<string>;
		staged: Map<string, DraftItem>;
		onstagedopen: (vm: VM) => void;
		oncontextvm?: (vm: VM, x: number, y: number) => void;
		// "ns/name" of the row the side peek is inspecting (the selection blue).
		activeKey?: string | null;
	} = $props();

	const cpuOf = (vm: VM) => vmSizing(vm, inventory.options).cpu;
	const memOf = (vm: VM) => vmSizing(vm, inventory.options).memory;

	let search = $state('');

	type SortKey =
		'power' | 'name' | 'namespace' | 'phase' | 'guestIP' | 'cpuCores' | 'memory' | 'sync';

	// Sort + filters survive reloads; the search box is transient on purpose.
	const prefs = persisted<{
		sortKey: SortKey;
		sortDir: 1 | -1;
		powerFilter: 'all' | Power;
		syncFilter: 'all' | SyncStatus;
	}>('dotvirt.vmtable', { sortKey: 'name', sortDir: 1, powerFilter: 'all', syncFilter: 'all' });

	function setSort(k: SortKey) {
		prefs.value =
			prefs.value.sortKey === k
				? { ...prefs.value, sortDir: prefs.value.sortDir === 1 ? -1 : 1 }
				: { ...prefs.value, sortKey: k, sortDir: 1 };
	}

	// Rank power/sync so the interesting states sort first (drift/problems at top).
	// Higher rank sorts earlier on ascending.
	const powerRank = (p: Power) => (p === 'On' ? 2 : p === 'Off' ? 1 : 0);
	const syncRank = (s: SyncStatus) =>
		s === 'OutOfSync' ? 4 : s === 'Pending' ? 3 : s === 'Unknown' ? 2 : s === 'NotTracked' ? 1 : 0;

	// Memory sorts numerically; an unreadable quantity sorts as zero.
	const memBytes = (m?: string) => (m && quantityBytes(m)) || 0;

	function cmp(a: VM, b: VM): number {
		const key = prefs.value.sortKey;
		switch (key) {
			case 'power':
				return powerRank(a.power) - powerRank(b.power);
			case 'sync':
				return syncRank(a.sync) - syncRank(b.sync);
			case 'cpuCores':
				return (cpuOf(a) ?? 0) - (cpuOf(b) ?? 0);
			case 'memory':
				return memBytes(memOf(a)) - memBytes(memOf(b));
			default: {
				const av = (a[key] ?? '').toString().toLowerCase();
				const bv = (b[key] ?? '').toString().toLowerCase();
				return av < bv ? -1 : av > bv ? 1 : 0;
			}
		}
	}

	const rows = $derived.by(() => {
		const q = search.trim().toLowerCase();
		const { powerFilter, syncFilter, sortDir } = prefs.value;
		const filtered = vms.filter((vm) => {
			if (powerFilter !== 'all' && vm.power !== powerFilter) return false;
			if (syncFilter !== 'all' && vm.sync !== syncFilter) return false;
			if (q) {
				const hay = `${vm.name} ${vm.namespace} ${vm.guestIP ?? ''}`.toLowerCase();
				if (!hay.includes(q)) return false;
			}
			return true;
		});
		// Stable tiebreak on name so equal sort keys don't jitter on live updates.
		return filtered.sort((a, b) => {
			const c = cmp(a, b) * sortDir;
			return c !== 0 ? c : a.name.localeCompare(b.name);
		});
	});

	// The two toolbar filters are structurally identical: one spec each, one
	// rendering below.
	const FILTERS: { key: 'powerFilter' | 'syncFilter'; title: string; options: string[][] }[] = [
		{
			key: 'powerFilter',
			title: 'Filter by power state',
			options: [
				['all', 'Power: all'],
				['On', 'On'],
				['Off', 'Off'],
				['Unknown', 'Unknown'],
			],
		},
		{
			key: 'syncFilter',
			title: 'Filter by ArgoCD sync status',
			options: [
				['all', 'Sync: all'],
				['Synced', 'Synced'],
				['OutOfSync', 'Out of sync'],
				['NotTracked', 'Not tracked'],
				['Pending', 'Pending sync'],
				['Unknown', 'Unknown'],
			],
		},
	];

	const cols: { key: SortKey; label: string; class?: string }[] = [
		{ key: 'power', label: '', class: 'w-8' },
		{ key: 'name', label: 'Name' },
		{ key: 'namespace', label: 'Namespace' },
		{ key: 'phase', label: 'Status' },
		{ key: 'guestIP', label: 'IP' },
		{ key: 'cpuCores', label: 'CPU', class: 'text-right' },
		{ key: 'memory', label: 'Memory', class: 'text-right' },
		{ key: 'sync', label: 'Sync' },
	];

	const allSelected = $derived(rows.length > 0 && rows.every((vm) => selected.has(vmKey(vm))));
	const someSelected = $derived(rows.some((vm) => selected.has(vmKey(vm))) && !allSelected);

	function toggleOne(vm: VM) {
		const k = vmKey(vm);
		const next = new Set(selected);
		next.has(k) ? next.delete(k) : next.add(k);
		selected = next;
	}

	function toggleAll() {
		const next = new Set(selected);
		if (allSelected) rows.forEach((vm) => next.delete(vmKey(vm)));
		else rows.forEach((vm) => next.add(vmKey(vm)));
		selected = next;
	}

	// Drop selected keys for VMs no longer present (deleted/scoped out), so the
	// action bar count never counts rows the user can't see.
	$effect(() => {
		const present = new Set(vms.map(vmKey));
		if ([...selected].some((k) => !present.has(k))) {
			selected = new Set([...selected].filter((k) => present.has(k)));
		}
	});
</script>

<div class="flex h-full flex-col">
	<div class="flex items-center gap-2 border-b border-line px-4 py-2">
		<TextInput bind:value={search} placeholder="Search name, namespace, IP…" width="w-64" />
		{#each FILTERS as f (f.key)}
			<SelectInput
				value={prefs.value[f.key]}
				onchange={(e) =>
					(prefs.value = {
						...prefs.value,
						[f.key]: e.currentTarget.value as 'all' | Power | SyncStatus,
					} as typeof prefs.value)}
				width="w-auto"
				class="text-ink-soft"
				title={f.title}
				aria-label={f.title}
			>
				{#each f.options as [value, label] (value)}
					<option {value}>{label}</option>
				{/each}
			</SelectInput>
		{/each}
		<span class="ml-auto text-xs text-ink-faint">{rows.length} VMs</span>
	</div>

	<div class="min-h-0 flex-1 overflow-auto">
		<table class="w-full text-[13px]">
			<thead class="sticky top-0 bg-inset text-left text-xs text-ink-muted">
				<tr class={THEAD_TR}>
					<th class="w-8 px-3 py-2">
						<input
							type="checkbox"
							checked={allSelected}
							indeterminate={someSelected}
							onchange={toggleAll}
							title="Select all (filtered)"
							aria-label="Select all (filtered)"
							class="cursor-pointer align-middle"
						/>
					</th>
					{#each cols as c (c.key)}
						<th class="px-3 py-2 font-medium {c.class ?? ''}">
							<button
								onclick={() => setSort(c.key)}
								aria-label="Sort by {c.label || 'power state'}"
								class="inline-flex items-center gap-1 hover:text-ink"
							>
								{c.label}
								{#if prefs.value.sortKey === c.key}
									{#if prefs.value.sortDir === 1}<ChevronUp
											size={12}
											class="text-ink-faint"
										/>{:else}<ChevronDown size={12} class="text-ink-faint" />{/if}
								{/if}
							</button>
						</th>
					{/each}
					<th class="px-3 py-2 font-medium">Health</th>
				</tr>
			</thead>
			<tbody class={TBODY}>
				{#each rows as vm (vm.namespace + '/' + vm.name)}
					{@const sc = staged.get(vm.namespace + '/' + vm.name)}
					<tr
						onclick={() => onselect(vm)}
						oncontextmenu={(e) => {
							if (!oncontextvm) return;
							e.preventDefault();
							oncontextvm(vm, e.clientX, e.clientY);
						}}
						class="cursor-pointer hover:bg-select-soft {activeKey === vmKey(vm)
							? 'bg-select hover:bg-select'
							: selected.has(vmKey(vm))
								? 'bg-select-soft'
								: ''}"
					>
						<td class="px-3 py-1.5">
							<input
								type="checkbox"
								checked={selected.has(vmKey(vm))}
								onclick={(e) => e.stopPropagation()}
								onchange={() => toggleOne(vm)}
								aria-label="Select {vm.name}"
								class="cursor-pointer align-middle"
							/>
						</td>
						<td class="px-3 py-1.5"><PowerDot power={vm.power} paused={vm.paused} /></td>
						<td
							class="px-3 py-1.5 font-medium {sc?.kind === 'delete'
								? 'text-ink-faint line-through'
								: 'text-ink'}"
						>
							<!-- A real link: the keyboard (and middle-click) path into the
							     detail the row's mouse onclick can't provide. -->
							<a
								href={vmHref(vm.namespace, vm.name)}
								onclick={(e) => e.stopPropagation()}
								class="hover:underline focus-visible:underline">{vm.name}</a
							>
						</td>
						<td class="px-3 py-1.5 text-ink-soft">{vm.namespace}</td>
						<td class="px-3 py-1.5">
							<StatusPill
								tone={phaseTone(vm.phase, vm.paused)}
								label={vm.paused ? 'Paused' : (vm.phase ?? '—')}
								dot={false}
							/>
						</td>
						<td class="px-3 py-1.5 font-mono text-xs text-ink-soft">{vm.guestIP ?? '—'}</td>
						<td class="px-3 py-1.5 text-right text-ink-soft" title={vm.instancetype}
							>{cpuOf(vm) ?? '—'}</td
						>
						<td class="px-3 py-1.5 text-right text-ink-soft" title={vm.instancetype}
							>{memOf(vm) ?? '—'}</td
						>
						<td class="px-3 py-1.5">
							{#if sc}
								<span class="inline-flex items-center gap-1.5">
									<StagedBadge item={sc} onopen={() => onstagedopen(vm)} />
									<SyncBadge sync={vm.sync} compact />
								</span>
							{:else}
								<SyncBadge sync={vm.sync} />
							{/if}
						</td>
						<td class="px-3 py-1.5 text-ink-soft">{vm.health ?? '—'}</td>
					</tr>
				{/each}
			</tbody>
		</table>

		{#if rows.length === 0}
			<div class="p-8 text-center text-sm text-ink-faint">
				{vms.length === 0 ? 'No VMs in scope.' : 'No VMs match the current filters.'}
			</div>
		{/if}
	</div>
</div>
