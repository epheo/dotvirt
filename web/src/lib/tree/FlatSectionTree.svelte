<script lang="ts">
	import { page } from '$app/state';
	import { LayoutGrid, Network } from '@lucide/svelte';
	import { type VM } from '#lib/api.js';
	import { vmNetworkKeys } from '#lib/lenses.js';
	import { hrefForScope, scopeFromPath } from '#lib/nav.js';
	import { networkByRef } from '#lib/networks.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import SyncBadge from '#lib/components/SyncBadge.svelte';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Networking tree: one group per NIC segment, a VM under every segment
	// it attaches (a VM with two NICs shows under both). Keys come from
	// $lib/lenses so the grid filter agrees.
	const ROOT = { label: 'All Networks', href: '/networking' };

	const scope = $derived(scopeFromPath(page.url.pathname));

	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	const groups = $derived.by(() => {
		const m = new Map<string, VM[]>();
		for (const vm of inventory.allVMs)
			for (const k of vmNetworkKeys(vm, inventory.networks)) {
				if (!m.has(k)) m.set(k, []);
				m.get(k)!.push(vm);
			}
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});
</script>

<div class="select-none text-[13px]">
	<TreeRow active={page.url.pathname === ROOT.href} alignChevron href={ROOT.href}>
		{#snippet icon()}
			<LayoutGrid size={14} class="text-side-dim" />
		{/snippet}
		<span class="truncate font-semibold text-side-ink">{ROOT.label}</span>
	</TreeRow>

	{#each groups as [key, vms] (key)}
		{@const gid = `network:${key}`}
		<div>
			<TreeRow
				active={scope.kind === 'network' && scope.network === key}
				expanded={!collapsed[gid]}
				ontoggle={() => toggle(gid)}
				href={hrefForScope({ kind: 'network', network: key })}
			>
				{#snippet icon()}
					<Network size={14} class="shrink-0 text-side-dim" />
				{/snippet}
				<span class="truncate font-semibold text-side-ink">{key}</span>
				{#snippet trailing()}
					{@const net = networkByRef(key, inventory.networks)}
					{#if net?.sync}<SyncBadge sync={net.sync} error={net.syncError} compact />{/if}
					<span class="text-xs text-side-dim">{vms.length}</span>
				{/snippet}
			</TreeRow>
			{#if !collapsed[gid]}
				{#each vms as vm (vm.namespace + '/' + vm.name)}
					<TreeVMRow {vm} indent={2} />
				{/each}
			{/if}
		</div>
	{/each}

	{#if groups.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No VMs in this view.</div>
	{/if}
</div>
