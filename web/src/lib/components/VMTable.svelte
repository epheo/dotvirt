<script lang="ts">
	import { ChevronDown, ChevronUp } from 'lucide-svelte';
	import type { DraftItem, Power, SyncStatus, VM } from '$lib/api';
	import { phaseTone } from '$lib/status';
	import { persisted } from '$lib/state/persisted.svelte';
	import PowerDot from './PowerDot.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import StatusPill from './StatusPill.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let {
		vms,
		onselect,
		selected = $bindable(new Set<string>()),
		staged,
		onstagedopen,
		oncontextvm,
	}: {
		vms: VM[];
		onselect: (vm: VM) => void;
		selected?: Set<string>;
		staged: Map<string, DraftItem>;
		onstagedopen: (vm: VM) => void;
		oncontextvm?: (vm: VM, x: number, y: number) => void;
	} = $props();

	const vmKey = (vm: VM) => `${vm.namespace}/${vm.name}`;

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

	// Rank power/sync so the interesting states sort first (drift/problems at top,
	// like vCenter). Higher rank sorts earlier on ascending.
	const powerRank = (p: Power) => (p === 'On' ? 2 : p === 'Off' ? 1 : 0);
	const syncRank = (s: SyncStatus) =>
		s === 'OutOfSync' ? 3 : s === 'Unknown' ? 2 : s === 'NotTracked' ? 1 : 0;

	// Memory like "2Gi"/"512Mi" → bytes, so the column sorts numerically not lexically.
	function memBytes(m?: string): number {
		if (!m) return 0;
		const match = /^(\d+(?:\.\d+)?)\s*([KMGT]i?)?B?$/.exec(m.trim());
		if (!match) return 0;
		const n = parseFloat(match[1]);
		const mult: Record<string, number> = {
			Ki: 1024,
			Mi: 1024 ** 2,
			Gi: 1024 ** 3,
			Ti: 1024 ** 4,
			K: 1e3,
			M: 1e6,
			G: 1e9,
			T: 1e12,
		};
		return n * (mult[match[2] ?? ''] ?? 1);
	}

	function cmp(a: VM, b: VM): number {
		const key = prefs.value.sortKey;
		switch (key) {
			case 'power':
				return powerRank(a.power) - powerRank(b.power);
			case 'sync':
				return syncRank(a.sync) - syncRank(b.sync);
			case 'cpuCores':
				return (a.cpuCores ?? 0) - (b.cpuCores ?? 0);
			case 'memory':
				return memBytes(a.memory) - memBytes(b.memory);
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

	// --- selection ---
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
		<input
			bind:value={search}
			placeholder="Search name, namespace, IP…"
			class="w-64 rounded border border-line-strong px-2 py-1 text-sm focus:border-blue-400"
		/>
		<select
			value={prefs.value.powerFilter}
			onchange={(e) =>
				(prefs.value = {
					...prefs.value,
					powerFilter: e.currentTarget.value as 'all' | Power,
				})}
			class="rounded border border-line-strong px-2 py-1 text-sm text-ink-soft"
			title="Filter by power state"
		>
			<option value="all">Power: all</option>
			<option value="On">On</option>
			<option value="Off">Off</option>
			<option value="Unknown">Unknown</option>
		</select>
		<select
			value={prefs.value.syncFilter}
			onchange={(e) =>
				(prefs.value = {
					...prefs.value,
					syncFilter: e.currentTarget.value as 'all' | SyncStatus,
				})}
			class="rounded border border-line-strong px-2 py-1 text-sm text-ink-soft"
			title="Filter by ArgoCD sync status"
		>
			<option value="all">Sync: all</option>
			<option value="Synced">Synced</option>
			<option value="OutOfSync">Out of sync</option>
			<option value="NotTracked">Not tracked</option>
			<option value="Unknown">Unknown</option>
		</select>
		<span class="ml-auto text-xs text-ink-faint">{rows.length} VMs</span>
	</div>

	<div class="min-h-0 flex-1 overflow-auto">
		<table class="w-full text-[13px]">
			<thead class="sticky top-0 bg-inset text-left text-xs text-ink-muted">
				<tr class="border-b border-line">
					<th class="w-8 px-3 py-2">
						<input
							type="checkbox"
							checked={allSelected}
							indeterminate={someSelected}
							onchange={toggleAll}
							title="Select all (filtered)"
							class="cursor-pointer align-middle"
						/>
					</th>
					{#each cols as c (c.key)}
						<th class="px-3 py-2 font-medium {c.class ?? ''}">
							<button
								onclick={() => setSort(c.key)}
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
			<tbody class="divide-y divide-line-soft">
				{#each rows as vm (vm.namespace + '/' + vm.name)}
					{@const sc = staged.get(vm.namespace + '/' + vm.name)}
					<tr
						onclick={() => onselect(vm)}
						oncontextmenu={(e) => {
							if (!oncontextvm) return;
							e.preventDefault();
							oncontextvm(vm, e.clientX, e.clientY);
						}}
						class="cursor-pointer hover:bg-blue-50 {selected.has(vmKey(vm)) ? 'bg-blue-50' : ''}"
					>
						<td class="px-3 py-1.5">
							<input
								type="checkbox"
								checked={selected.has(vmKey(vm))}
								onclick={(e) => e.stopPropagation()}
								onchange={() => toggleOne(vm)}
								class="cursor-pointer align-middle"
							/>
						</td>
						<td class="px-3 py-1.5"><PowerDot power={vm.power} paused={vm.paused} /></td>
						<td
							class="px-3 py-1.5 font-medium {sc?.kind === 'delete'
								? 'text-ink-faint line-through'
								: 'text-ink'}">{vm.name}</td
						>
						<td class="px-3 py-1.5 text-ink-soft">{vm.namespace}</td>
						<td class="px-3 py-1.5">
							<StatusPill
								tone={phaseTone(vm.phase, vm.paused)}
								label={vm.paused ? 'Paused' : (vm.phase ?? '—')}
								dot={false}
							/>
						</td>
						<td class="px-3 py-1.5 font-mono text-xs text-ink-soft">{vm.guestIP ?? '—'}</td>
						<td class="px-3 py-1.5 text-right text-ink-soft">{vm.cpuCores ?? '—'}</td>
						<td class="px-3 py-1.5 text-right text-ink-soft">{vm.memory ?? '—'}</td>
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
