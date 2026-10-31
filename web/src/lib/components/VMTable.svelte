<script lang="ts">
	import { ChevronDown, ChevronUp } from '@lucide/svelte';
	import {
		vmKey,
		type DraftItem,
		type Options,
		type Power,
		type SyncStatus,
		type VM,
	} from '#lib/api.js';
	import { quantityBytes } from '#lib/format.js';
	import { vmSizing } from '#lib/sizing.js';
	import { phaseTone } from '#lib/status.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { TBODY, THEAD_TR } from '#lib/table.js';
	import PowerDot from './PowerDot.svelte';
	import { vmHref } from '#lib/nav.js';
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

	// Sizing once per VM object, not per sort comparison: inventory.apply keeps
	// unchanged VMs identical across frames, so the cache survives live updates.
	type RowSize = { cpu?: number; memory?: string; memBytes: number };
	let sizes = new WeakMap<VM, RowSize>();
	let sizesFor: Options | null = null;
	function sizeOf(vm: VM): RowSize {
		const options = inventory.options;
		if (options !== sizesFor) {
			sizes = new WeakMap();
			sizesFor = options;
		}
		let s = sizes.get(vm);
		if (!s) {
			const { cpu, memory } = vmSizing(vm, options);
			s = { cpu, memory, memBytes: (memory && quantityBytes(memory)) || 0 };
			sizes.set(vm, s);
		}
		return s;
	}

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

	function cmp(a: VM, b: VM): number {
		const key = prefs.value.sortKey;
		switch (key) {
			case 'power':
				return powerRank(a.power) - powerRank(b.power);
			case 'sync':
				return syncRank(a.sync) - syncRank(b.sync);
			case 'cpuCores':
				return (sizeOf(a).cpu ?? 0) - (sizeOf(b).cpu ?? 0);
			case 'memory':
				return sizeOf(a).memBytes - sizeOf(b).memBytes;
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

	// Roving tabindex: one row is in the tab order, arrows move focus between
	// rows. Falls back to the peeked row, then the first, when the focused VM
	// leaves the filtered set.
	let focusKey = $state<string | null>(null);
	const tabKey = $derived.by(() => {
		const keys = new Set(rows.map(vmKey));
		if (focusKey && keys.has(focusKey)) return focusKey;
		if (activeKey && keys.has(activeKey)) return activeKey;
		return rows.length ? vmKey(rows[0]) : null;
	});

	function onrowkeydown(e: KeyboardEvent, vm: VM) {
		// The checkbox and the name link keep their own keys.
		if (e.target !== e.currentTarget) return;
		const row = e.currentTarget as HTMLElement;
		const body = row.parentElement!;
		const target = {
			ArrowDown: row.nextElementSibling,
			ArrowUp: row.previousElementSibling,
			Home: body.firstElementChild,
			End: body.lastElementChild,
		}[e.key];
		if (target !== undefined) {
			e.preventDefault();
			(target as HTMLElement | null)?.focus();
		} else if (e.key === 'Enter') {
			// On the peeked row Enter reaches the inspector, which opens the full page.
			if (vmKey(vm) === activeKey) return;
			e.preventDefault();
			e.stopPropagation();
			onselect(vm);
		} else if (e.key === ' ') {
			e.preventDefault();
			toggleOne(vm);
		}
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
		<table class="w-full text-[13px]" role="grid" aria-multiselectable="true" aria-label="VMs">
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
					{@const k = vmKey(vm)}
					{@const sc = staged.get(k)}
					{@const size = sizeOf(vm)}
					<tr
						tabindex={k === tabKey ? 0 : -1}
						aria-selected={selected.has(k)}
						onfocus={() => (focusKey = k)}
						onkeydown={(e) => onrowkeydown(e, vm)}
						onclick={() => onselect(vm)}
						oncontextmenu={(e) => {
							if (!oncontextvm) return;
							e.preventDefault();
							oncontextvm(vm, e.clientX, e.clientY);
						}}
						class="cursor-pointer hover:bg-select-soft {activeKey === k
							? 'bg-select hover:bg-select'
							: selected.has(k)
								? 'bg-select-soft'
								: ''}"
					>
						<td class="px-3 py-1.5">
							<input
								type="checkbox"
								checked={selected.has(k)}
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
							>{size.cpu ?? '—'}</td
						>
						<td class="px-3 py-1.5 text-right text-ink-soft" title={vm.instancetype}
							>{size.memory ?? '—'}</td
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
