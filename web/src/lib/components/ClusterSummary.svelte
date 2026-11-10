<script lang="ts">
	import {
		api,
		vmKey,
		type ClusterSummary,
		type HostCapacity,
		type Node,
		type VM,
	} from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { headroom } from '#lib/headroom.js';
	import { metricsGap } from '#lib/metrics.js';
	import { resource } from '#lib/resource.svelte.js';
	import type { HostFacts } from '#lib/state/hosts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { computeRoot, vmsOff } from '#lib/state/modules.svelte.js';
	import CapacityCard from './CapacityCard.svelte';
	import IssuesCard from './IssuesCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import ScopeChanges from './ScopeChanges.svelte';
	import TopConsumers from './TopConsumers.svelte';

	// A scope's Summary, as cards on the page ground: what needs a person,
	// how full the hosts are in real units and how many more VMs fit (metrics,
	// when present), who uses the most, and what is changing. The page header
	// above says what the VMs are doing.
	let {
		scope = {},
		vms,
		onselect,
		hosts,
	}: {
		scope?: { project?: string; namespace?: string; node?: string };
		vms: VM[];
		onselect?: (namespace: string, name: string) => void;
		// A Hosts page's own reads of node state and capacity, shared with its
		// header; without them this summary reads its own.
		hosts?: HostFacts;
	} = $props();

	// Usage and top consumers are KubeVirt's VM metrics: without KubeVirt they
	// could only read zero against the hosts' totals, so neither card shows and
	// neither is read. An unknown state keeps both.
	const vmMetrics = $derived(!vmsOff());

	// Keyed on a stable scope string; old data stays up while a scope switch loads.
	const scopeKey = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}|${scope.node ?? ''}`);
	const summary = resource<ClusterSummary | null>(
		() => (vmMetrics ? scopeKey : ''),
		(k) => (k ? api.clusterSummary(scope) : Promise.resolve(null)),
		{ poll: () => (vmMetrics ? 30000 : 0) },
	);
	const data = $derived(summary.data);

	// Headroom is a cluster fact, read beside the scope's own numbers. Node
	// state decides which hosts take new VMs; without node read rights every
	// capacity host counts.
	const ownCap = resource<HostCapacity | null>(
		() => (hosts || !vmMetrics ? '' : 'capacity'),
		(k) => (k ? api.capacity() : Promise.resolve(null)),
		{ poll: () => (hosts || !vmMetrics ? 0 : 30000) },
	);
	const ownNodes = resource<Node[] | null>(
		() => (hosts || !vmMetrics || !clusterGate.hosts().ok ? '' : 'nodes'),
		(k) => (k ? api.nodes() : Promise.resolve(null)),
	);
	const capRes = $derived(hosts?.cap ?? ownCap);
	const nodesRes = $derived(hosts?.nodes ?? ownNodes);
	// A capacity read with no numbers is no capacity: "room for 0" would be a lie.
	const capData = $derived(
		capRes.failed || !capRes.data || metricsGap(capRes.data.unavailable) ? null : capRes.data,
	);
	const room = $derived.by(() => {
		if (!capData) return null;
		const c = scope.node
			? { ...capData, nodes: capData.nodes.filter((n) => n.node === scope.node) }
			: capData;
		return headroom(c, nodesRes.failed ? null : nodesRes.data, vms, inventory.options);
	});

	const host = $derived(
		scope.node ? { name: scope.node, vms: new Set(vms.map(vmKey)) } : undefined,
	);
</script>

<div class="grid items-start gap-4 lg:grid-cols-2">
	<IssuesCard
		scope={{ project: scope.project, namespace: scope.namespace }}
		{host}
		label={scope.namespace ?? scope.project ?? scope.node ?? computeRoot()}
	/>
	{#if vmMetrics}
		<CapacityCard {data} loading={summary.loading} failed={summary.failed} {room} cap={capData} />
		{#if data && !metricsGap(data.unavailable)}
			<TopConsumers topCpu={data.topCpu} topMemory={data.topMemory} {vms} {onselect} />
		{/if}
	{/if}
	<ScopeChanges scope={{ project: scope.project, namespace: scope.namespace }} />
	<!-- Quota-aware capacity: ResourceQuota bars at project/namespace scope -
	     the tenant's real boundary, where node-allocatable is the cluster's. -->
	{#if scope.project || scope.namespace}
		<QuotaBand scope={{ project: scope.project, namespace: scope.namespace }} card />
	{/if}
</div>
