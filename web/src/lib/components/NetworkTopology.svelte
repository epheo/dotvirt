<script lang="ts">
	import { Network, Radio, Router } from 'lucide-svelte';
	import type { Network as PortGroup, Project, Uplink, VM } from '$lib/api';
	import { vmNetworkKeys, POD_NETWORK, NO_NETWORK } from '$lib/lenses';
	import { segmentType, TERMS } from '$lib/vocab';
	import PowerDot from './PowerDot.svelte';
	import StatusDot from './StatusDot.svelte';

	// The Network Topology map — NSX-T's signature screen, built entirely from the
	// catalog dotvirt already returns: the platform provider edge (Tier-0) → each
	// project's router (Tier-1, its primary segment) → segments → VMs. It owns no new
	// data; segment membership reuses the same vmNetworkKeys the Segments lens groups
	// by, so the map and the tree can never disagree.
	let {
		networks = [],
		uplinks = [],
		vms = [],
		projects = [],
		onpick,
	}: {
		networks?: PortGroup[];
		uplinks?: Uplink[];
		vms?: VM[];
		projects?: Project[];
		onpick: (network: string) => void; // scope the grid to a segment's VMs
	} = $props();

	// VMs attached to each segment, keyed by the segment name the lens groups by.
	const vmsByKey = $derived.by(() => {
		const m = new Map<string, VM[]>();
		for (const vm of vms)
			for (const k of vmNetworkKeys(vm, networks)) {
				const arr = m.get(k);
				if (arr) arr.push(vm);
				else m.set(k, [vm]);
			}
		return m;
	});
	const vmsFor = (name: string): VM[] => vmsByKey.get(name) ?? [];

	// The Segments lens (and grid scope) identify a port group by name, so the map
	// collapses same-named networks to one card too. This keeps the map and the tree
	// in agreement (a click scopes the grid by name), and avoids handing an {#each} a
	// duplicate key — two project networks sharing a name across a project's namespaces
	// otherwise crash the render in Svelte 5.
	const byName = (nets: PortGroup[]): PortGroup[] => [
		...new Map(nets.map((n) => [n.name, n])).values(),
	];

	// Provider-edge (Tier-0) segments: cluster-scoped CUDNs — a shared overlay or a
	// VLAN localnet bridged to an uplink.
	const t0Segments = $derived(
		byName(networks.filter((n) => n.scope === 'shared' || n.kind === 'vlan')),
	);

	// A project's (Tier-1's) own segments: its primary "VM Network" and any
	// project-scoped overlay segments it owns.
	function projSegments(p: Project): { primary: PortGroup[]; overlays: PortGroup[] } {
		const ns = new Set(p.namespaces.map((n) => n.namespace));
		const own = networks.filter((n) => n.scope === 'project' && n.namespace && ns.has(n.namespace));
		return {
			primary: byName(own.filter((n) => n.kind === 'default')),
			overlays: byName(own.filter((n) => n.kind !== 'default')),
		};
	}

	const podVMs = $derived(vmsFor(POD_NETWORK));
	const noVMs = $derived(vmsFor(NO_NETWORK));
</script>

{#snippet segmentCard(net: PortGroup)}
	{@const st = segmentType(net)}
	{@const list = vmsFor(net.name)}
	<button
		onclick={() => onpick(net.name)}
		class="flex w-full flex-col gap-1 rounded border border-line bg-panel px-3 py-2 text-left hover:border-accent/60 hover:bg-select-soft"
	>
		<div class="flex flex-wrap items-center gap-2">
			<Network size={13} class="shrink-0 text-ink-faint" />
			<span class="font-medium text-ink-soft">{net.name}</span>
			<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[10px] text-ink-muted"
				>{st.nsx} · {st.vsphere}</span
			>
			<!-- Plain dot, not the clickable SyncBadge: the card is itself a button, and
			     nesting another would be invalid; the segment page holds the detail. -->
			{#if net.sync === 'OutOfSync'}
				<StatusDot tone="danger" size="xs" title={net.syncError || 'ArgoCD: out of sync'} />
			{/if}
			{#if net.vlan}<span class="rounded bg-warn-soft px-1.5 py-0.5 text-[10px] text-warn-ink"
					>VLAN {net.vlan}</span
				>{/if}
			{#if net.uplink}<span class="text-[10px] text-ink-faint">↑ {net.uplink}</span>{/if}
			{#if net.subnets?.length}<span class="text-[10px] text-ink-faint"
					>{net.subnets.join(', ')}</span
				>{/if}
			<span class="ml-auto text-xs text-ink-faint"
				>{list.length} VM{list.length === 1 ? '' : 's'}</span
			>
		</div>
		{#if list.length}
			<div class="flex flex-wrap gap-1 pl-5">
				{#each list.slice(0, 14) as vm (vm.namespace + '/' + vm.name)}
					<span
						class="inline-flex items-center gap-1 rounded bg-inset px-1.5 py-0.5 text-[11px] text-ink-soft"
					>
						<PowerDot power={vm.power} paused={vm.paused} />{vm.name}
					</span>
				{/each}
				{#if list.length > 14}<span class="px-1 text-[11px] text-ink-faint"
						>+{list.length - 14} more</span
					>{/if}
			</div>
		{/if}
	</button>
{/snippet}

<div class="max-w-5xl space-y-5 p-5 text-[13px]">
	<p class="text-xs text-ink-muted">
		{TERMS.tier0.nsx} (provider edge) → {TERMS.tier1.nsx} (project router) → {TERMS.segment.nsx}s ({TERMS
			.segment.vsphere}s) → VMs. Overlay segments are isolated islands — there is no in-overlay
		router between them yet, so cross-segment traffic exits to the fabric.
	</p>

	<!-- Tier-0: the platform provider edge — uplinks (transports) and cluster-scoped segments. -->
	<section class="rounded-lg border border-line-strong bg-inset p-3">
		<div class="mb-2 flex items-center gap-2">
			<Radio size={15} class="text-ink-muted" />
			<span class="font-semibold text-ink-soft">{TERMS.tier0.nsx}</span>
			<span class="text-xs text-ink-faint">· {TERMS.tier0.vsphere}</span>
		</div>
		{#if uplinks.length}
			<div class="mb-2 flex flex-wrap gap-1.5 pl-6">
				{#each uplinks as u (u.name)}
					<span class="rounded border border-line bg-panel px-2 py-0.5 text-[11px] text-ink-soft">
						{u.name}{u.builtin ? ' · default' : ''} <span class="text-ink-faint">({u.bridge})</span>
					</span>
				{/each}
			</div>
		{/if}
		<div class="space-y-1.5 border-l-2 border-line-strong pl-4">
			{#each t0Segments as net (net.name)}{@render segmentCard(net)}{/each}
			{#if t0Segments.length === 0}
				<p class="text-xs text-ink-faint italic">No provider-edge segments yet.</p>
			{/if}
		</div>
	</section>

	<!-- Tier-1: one gateway per project, carrying its primary + overlay segments. -->
	{#each projects as p (p.name)}
		{@const segs = projSegments(p)}
		<section class="rounded-lg border border-line p-3">
			<div class="mb-2 flex items-center gap-2">
				<Router size={15} class="text-accent" />
				<span class="font-semibold text-ink-soft">{TERMS.tier1.nsx}</span>
				<span class="text-xs text-ink-faint">· {p.name} ({TERMS.tier1.vsphere})</span>
			</div>
			<div class="space-y-1.5 border-l-2 border-select pl-4">
				{#each segs.primary as net (net.name)}{@render segmentCard(net)}{/each}
				{#each segs.overlays as net (net.name)}{@render segmentCard(net)}{/each}
				{#if !segs.primary.length && !segs.overlays.length}
					<p class="text-xs text-ink-faint italic">No segments — VMs ride the pod network.</p>
				{/if}
			</div>
		</section>
	{/each}

	<!-- VMs with no user-defined segment: the cluster pod network or no NIC at all. -->
	{#if podVMs.length || noVMs.length}
		<section class="rounded-lg border border-line p-3">
			<div class="mb-2 flex items-center gap-2">
				<Network size={15} class="text-ink-faint" />
				<span class="font-semibold text-ink-soft">Unsegmented</span>
				<span class="text-xs text-ink-faint">· cluster pod network</span>
			</div>
			<div class="flex flex-wrap gap-1 pl-6">
				{#each [...podVMs, ...noVMs] as vm (vm.namespace + '/' + vm.name)}
					<span
						class="inline-flex items-center gap-1 rounded bg-inset px-1.5 py-0.5 text-[11px] text-ink-soft"
					>
						<PowerDot power={vm.power} paused={vm.paused} />{vm.name}
					</span>
				{/each}
			</div>
		</section>
	{/if}
</div>
