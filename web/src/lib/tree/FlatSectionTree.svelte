<script lang="ts">
	import { page } from '$app/state';
	import { Database, LayoutGrid, Network, Server, Shield, Workflow } from 'lucide-svelte';
	import { api, type Node, type VM } from '$lib/api';
	import { vmNetworkKeys, vmStorageKeys, type Scope } from '$lib/lenses';
	import { hrefForScope, scopeFromPath } from '$lib/nav';
	import { networkByRef } from '$lib/networks';
	import { resource } from '$lib/resource.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import SyncBadge from '$lib/components/SyncBadge.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The flat section trees - Hosts (physical placement), Networking (by NIC
	// segment), Storage (by dataVolume class): one group per key, a VM under
	// every key it matches (a VM with two NICs shows under both segments).
	// Keys come from $lib/lenses so the grid filter agrees.
	let { kind }: { kind: 'node' | 'network' | 'storage' } = $props();

	const scope = $derived(scopeFromPath(page.url.pathname));

	// The section home row: Hosts/Storage roll up to the whole inventory;
	// Networking's home is the Topology map.
	const ROOT = $derived(
		{
			node: { label: 'All Nodes', href: '/hosts' },
			network: { label: 'Topology', href: '/networking' },
			storage: { label: 'All Storage', href: '/storage' },
		}[kind],
	);

	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	// Hosts exist without VMs: seed the node groups from the host list so an
	// empty (fresh, or fully drained) host still has a row. Caller RBAC gates the
	// list - a tenant without node-read keeps the VM-derived groups only. kind is
	// fixed per instance (the layout passes a literal), so reading it at init is
	// the intent and the conditional resource is init-stable.
	// svelte-ignore state_referenced_locally
	const nodesRes = kind === 'node' ? resource<Node[]>(() => 'nodes', api.nodes) : null;

	const groups = $derived.by(() => {
		const m = new Map<string, VM[]>();
		const add = (key: string, vm: VM) => {
			if (!m.has(key)) m.set(key, []);
			m.get(key)!.push(vm);
		};
		for (const node of nodesRes?.data ?? []) m.set(node.name, []);
		for (const vm of inventory.allVMs) {
			if (kind === 'node') add(vm.nodeName || '(unscheduled)', vm);
			else if (kind === 'network')
				for (const k of vmNetworkKeys(vm, inventory.networks)) add(k, vm);
			else for (const k of vmStorageKeys(vm, inventory.defaultStorageClass)) add(k, vm);
		}
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});

	const groupScope = (key: string): Scope =>
		kind === 'node'
			? { kind: 'node', node: key }
			: kind === 'network'
				? { kind: 'network', network: key }
				: { kind: 'storage', storageClass: key };
	const groupScoped = (key: string) =>
		(scope.kind === 'node' && scope.node === key) ||
		(scope.kind === 'network' && scope.network === key) ||
		(scope.kind === 'storage' && scope.storageClass === key);
</script>

<div class="select-none text-[13px]">
	<TreeRow active={page.url.pathname === ROOT.href} alignChevron href={ROOT.href}>
		{#snippet icon()}
			{#if kind === 'network'}<Workflow size={14} class="text-ink-faint" />
			{:else}<LayoutGrid size={14} class="text-ink-faint" />{/if}
		{/snippet}
		<span class="truncate font-semibold text-ink-soft">{ROOT.label}</span>
	</TreeRow>

	{#if kind === 'network'}
		<TreeRow
			active={page.url.pathname === '/networking/security'}
			alignChevron
			href="/networking/security"
		>
			{#snippet icon()}<Shield size={14} class="text-ink-faint" />{/snippet}
			<span class="truncate font-semibold text-ink-soft">Security</span>
		</TreeRow>
	{/if}

	{#each groups as [key, vms] (key)}
		{@const gid = `${kind}:${key}`}
		<div>
			<TreeRow
				active={groupScoped(key)}
				expanded={!collapsed[gid]}
				ontoggle={() => toggle(gid)}
				href={hrefForScope(groupScope(key))}
			>
				{#snippet icon()}
					{#if kind === 'node'}<Server size={14} class="shrink-0 text-ink-muted" />
					{:else if kind === 'network'}<Network size={14} class="shrink-0 text-ink-muted" />
					{:else}<Database size={14} class="shrink-0 text-ink-muted" />{/if}
				{/snippet}
				<span class="truncate font-semibold text-ink-soft">{key}</span>
				{#snippet trailing()}
					{#if kind === 'network'}
						{@const net = networkByRef(key, inventory.networks)}
						{#if net?.sync}<SyncBadge sync={net.sync} error={net.syncError} compact />{/if}
					{/if}
					<span class="text-xs text-ink-faint">{vms.length}</span>
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
		<div class="px-2 py-4 text-center text-xs text-ink-faint">No VMs in this view.</div>
	{/if}
</div>
