<script lang="ts">
	import { page } from '$app/state';
	import { Server, Wrench } from '@lucide/svelte';
	import { api, type Node, type VM } from '#lib/api.js';
	import { hrefForScope, scopeFromPath } from '#lib/nav.js';
	import { byName, LEAF_LIMIT, leafWindow, matches, SMALL_FLEET } from '#lib/rail.js';
	import { resource } from '#lib/resource.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import RailFilter from './RailFilter.svelte';
	import RailMore from './RailMore.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Hosts tree: every host with its VM count and maintenance marked, and
	// at most LEAF_LIMIT VMs under an open host with "N more" opening its VMs
	// tab. Hosts are bounded by the cluster, so they never fold; VMs not placed
	// on any host sit last.
	const UNPLACED = '(unscheduled)';
	const scope = $derived(scopeFromPath(page.url.pathname));
	const small = $derived(inventory.vmCount <= SMALL_FLEET);
	const fmt = (n: number) => n.toLocaleString('en-US');

	// Hosts exist without VMs: seed the groups from the host list so an empty
	// (fresh, or fully drained) host still has a row. Caller RBAC gates the
	// list; a tenant without node-read keeps the VM-derived groups only.
	const nodesRes = resource<Node[]>(() => 'nodes', api.nodes);
	const nodeOf = $derived(new Map((nodesRes.data ?? []).map((n) => [n.name, n])));

	const groups = $derived.by(() => {
		const m = new Map<string, VM[]>();
		for (const n of nodesRes.data ?? []) m.set(n.name, []);
		for (const vm of inventory.allVMs) {
			const key = vm.nodeName || UNPLACED;
			if (!m.has(key)) m.set(key, []);
			m.get(key)!.push(vm);
		}
		const hosts = byName(
			[...m.entries()].filter(([k]) => k !== UNPLACED),
			([k]) => k,
		);
		const unplaced = m.get(UNPLACED);
		return unplaced ? [...hosts, [UNPLACED, unplaced] as [string, VM[]]] : hosts;
	});
	const hostCount = $derived(groups.filter(([k]) => k !== UNPLACED).length);

	let query = $state('');
	const shown = $derived(groups.filter(([k]) => matches(query, k)));

	const vmInView = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		return parts[0] === 'vm' && parts.length >= 3 ? `${parts[1]}/${parts[2]}` : null;
	});
	const hostInView = $derived(
		scope.kind === 'node'
			? scope.node
			: vmInView
				? (inventory.allVMs.find((v) => `${v.namespace}/${v.name}` === vmInView)?.nodeName ??
					UNPLACED)
				: undefined,
	);

	// Unset means the default: open in a small fleet or on the path to what is
	// in view. An explicit toggle wins and persists.
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.hosts', {});
	const isOpen = (key: string) => {
		const v = collapsed.value[key];
		return v === undefined ? small || key === hostInView : !v;
	};
	const setOpen = (key: string, open: boolean) =>
		(collapsed.value = { ...collapsed.value, [key]: !open });
</script>

<div class="flex flex-col gap-0.5 px-2.5 pb-3 text-[13px] select-none">
	<RailFilter bind:value={query} label="Filter hosts" placeholder="Filter {hostCount} hosts" />

	<TreeRow active={page.url.pathname === '/hosts'} href="/hosts">
		<span class="truncate text-side-ink">All Nodes</span>
		{#snippet trailing()}<span class="text-xs text-side-dim">{fmt(hostCount)}</span>{/snippet}
	</TreeRow>

	{#if groups.length > 0}
		<div class="px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase">
			Hosts
		</div>
	{/if}

	{#each shown as [key, vms] (key)}
		{@const open = isOpen(key)}
		{@const href = hrefForScope({ kind: 'node', node: key })}
		{@const leaves = leafWindow(vms, LEAF_LIMIT, (v) => `${v.namespace}/${v.name}` === vmInView)}
		<TreeRow
			active={scope.kind === 'node' && scope.node === key}
			expanded={open}
			ontoggle={() => setOpen(key, !open)}
			{href}
		>
			{#snippet icon()}
				<Server size={14} class="shrink-0 text-side-dim" />
			{/snippet}
			<span class="truncate {open ? 'text-side-ink' : ''}">{key}</span>
			{#snippet trailing()}
				{#if nodeOf.get(key)?.maintenance}
					<span class="flex items-center gap-1 text-[11px] text-warn" title="In maintenance">
						<Wrench size={12} />maintenance
					</span>
				{:else if nodeOf.get(key)?.unschedulable}
					<span class="text-[11px] text-warn" title="Takes no new VMs">cordoned</span>
				{/if}
				<span class="text-xs text-side-dim">{fmt(vms.length)}</span>
			{/snippet}
		</TreeRow>
		{#if open}
			{#each leaves.shown as vm (vm.namespace + '/' + vm.name)}
				<TreeVMRow {vm} indent={2} />
			{/each}
			{#if leaves.more > 0}
				<RailMore href="{href}?tab=vms" indent={2}>{fmt(leaves.more)} more VMs</RailMore>
			{/if}
		{/if}
	{/each}

	{#if query.trim() && shown.length === 0}
		<div class="px-2 py-3 text-xs text-side-dim">No host matches.</div>
	{:else if groups.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No VMs in this view.</div>
	{/if}
</div>
