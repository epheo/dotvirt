<script lang="ts">
	import type { DraftItem, Power, SyncStatus, VM } from '$lib/api';
	import PowerDot from './PowerDot.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let {
		vms,
		onselect,
		selected = $bindable(new Set<string>()),
		staged,
		onstagedopen
	}: {
		vms: VM[];
		onselect: (vm: VM) => void;
		selected?: Set<string>;
		staged: Map<string, DraftItem>;
		onstagedopen: (vm: VM) => void;
	} = $props();

	const vmKey = (vm: VM) => `${vm.namespace}/${vm.name}`;

	let search = $state('');
	let powerFilter = $state<'all' | Power>('all');
	let syncFilter = $state<'all' | SyncStatus>('all');

	type SortKey = 'power' | 'name' | 'namespace' | 'phase' | 'guestIP' | 'cpuCores' | 'memory' | 'sync';
	let sortKey = $state<SortKey>('name');
	let sortDir = $state<1 | -1>(1);

	function setSort(k: SortKey) {
		if (sortKey === k) sortDir = sortDir === 1 ? -1 : 1;
		else {
			sortKey = k;
			sortDir = 1;
		}
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
			Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4,
			K: 1e3, M: 1e6, G: 1e9, T: 1e12
		};
		return n * (mult[match[2] ?? ''] ?? 1);
	}

	function cmp(a: VM, b: VM): number {
		switch (sortKey) {
			case 'power':
				return powerRank(a.power) - powerRank(b.power);
			case 'sync':
				return syncRank(a.sync) - syncRank(b.sync);
			case 'cpuCores':
				return (a.cpuCores ?? 0) - (b.cpuCores ?? 0);
			case 'memory':
				return memBytes(a.memory) - memBytes(b.memory);
			default: {
				const av = (a[sortKey] ?? '').toString().toLowerCase();
				const bv = (b[sortKey] ?? '').toString().toLowerCase();
				return av < bv ? -1 : av > bv ? 1 : 0;
			}
		}
	}

	const rows = $derived.by(() => {
		const q = search.trim().toLowerCase();
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
		{ key: 'sync', label: 'Sync' }
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
	<div class="flex items-center gap-2 border-b border-slate-200 px-4 py-2">
		<input
			bind:value={search}
			placeholder="Search name, namespace, IP…"
			class="w-64 rounded border border-slate-300 px-2 py-1 text-sm focus:border-blue-400 focus:outline-none"
		/>
		<select
			bind:value={powerFilter}
			class="rounded border border-slate-300 px-2 py-1 text-sm text-slate-700"
			title="Filter by power state"
		>
			<option value="all">Power: all</option>
			<option value="On">On</option>
			<option value="Off">Off</option>
			<option value="Unknown">Unknown</option>
		</select>
		<select
			bind:value={syncFilter}
			class="rounded border border-slate-300 px-2 py-1 text-sm text-slate-700"
			title="Filter by ArgoCD sync status"
		>
			<option value="all">Sync: all</option>
			<option value="Synced">Synced</option>
			<option value="OutOfSync">OutOfSync</option>
			<option value="NotTracked">Not tracked</option>
			<option value="Unknown">Unknown</option>
		</select>
		<span class="ml-auto text-xs text-slate-400">{rows.length} VMs</span>
	</div>

	<div class="min-h-0 flex-1 overflow-auto">
		<table class="w-full text-[13px]">
			<thead class="sticky top-0 bg-slate-50 text-left text-xs text-slate-500">
				<tr class="border-b border-slate-200">
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
								class="inline-flex items-center gap-1 hover:text-slate-800"
							>
								{c.label}
								{#if sortKey === c.key}<span class="text-slate-400">{sortDir === 1 ? '▲' : '▼'}</span>{/if}
							</button>
						</th>
						{/each}
					<th class="px-3 py-2 font-medium">Health</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100">
				{#each rows as vm (vm.namespace + '/' + vm.name)}
					{@const sc = staged.get(vm.namespace + '/' + vm.name)}
					<tr
						onclick={() => onselect(vm)}
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
						<td class="px-3 py-1.5 font-medium {sc?.kind === 'delete' ? 'text-slate-400 line-through' : 'text-slate-800'}">{vm.name}</td>
						<td class="px-3 py-1.5 text-slate-600">{vm.namespace}</td>
						<td class="px-3 py-1.5 text-slate-600">{vm.paused ? 'Paused' : (vm.phase ?? '—')}</td>
						<td class="px-3 py-1.5 font-mono text-xs text-slate-600">{vm.guestIP ?? '—'}</td>
						<td class="px-3 py-1.5 text-right text-slate-700">{vm.cpuCores ?? '—'}</td>
						<td class="px-3 py-1.5 text-right text-slate-700">{vm.memory ?? '—'}</td>
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
						<td class="px-3 py-1.5 text-slate-600">{vm.health ?? '—'}</td>
					</tr>
				{/each}
			</tbody>
		</table>

		{#if rows.length === 0}
			<div class="p-8 text-center text-sm text-slate-400">
				{vms.length === 0 ? 'No VMs in scope.' : 'No VMs match the current filters.'}
			</div>
		{/if}
	</div>
</div>
