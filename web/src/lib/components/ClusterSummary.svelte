<script lang="ts">
	import { untrack } from 'svelte';
	import { GitPullRequest } from '@lucide/svelte';
	import {
		api,
		vmKey,
		type ClusterSummary,
		type HostCapacity,
		type Node,
		type VM,
	} from '#lib/api.js';
	import { headroom } from '#lib/headroom.js';
	import { metricsGap } from '#lib/metrics.js';
	import { resource } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { TONE_DOT } from '#lib/status.js';
	import CapacityCard from './CapacityCard.svelte';
	import IssuesCard from './IssuesCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import ScopeChanges from './ScopeChanges.svelte';
	import TopConsumers from './TopConsumers.svelte';

	// A scope's Summary answers three questions in order: what runs here and
	// what needs a person (live stream, always), how full the hosts are in real
	// units, and how many more VMs fit (metrics, when present).
	let {
		scope = {},
		vms,
		onselect,
		total = true,
	}: {
		scope?: { project?: string; namespace?: string; node?: string };
		vms: VM[];
		onselect?: (namespace: string, name: string) => void;
		// false when the page header above already says how many VMs there are.
		total?: boolean;
	} = $props();

	// Keyed on a stable scope string; old data stays up while a scope switch loads.
	const scopeKey = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}|${scope.node ?? ''}`);
	const summary = resource<ClusterSummary>(
		() => scopeKey,
		() => api.clusterSummary(scope),
		{ poll: 30000 },
	);
	const data = $derived(summary.data);

	// Headroom is a cluster fact, read beside the scope's own numbers. Node
	// state decides which hosts take new VMs; without node read rights every
	// capacity host counts.
	const cap = resource<HostCapacity>(
		() => '',
		() => api.capacity(),
		{ poll: 30000 },
	);
	// A capacity read with no numbers is no capacity: "room for 0" would be a lie.
	const capData = $derived(
		cap.failed || !cap.data || metricsGap(cap.data.unavailable) ? null : cap.data,
	);
	const nodesRes = resource<Node[]>(() => 'nodes', api.nodes);
	const room = $derived.by(() => {
		if (!capData) return null;
		const c = scope.node
			? { ...capData, nodes: capData.nodes.filter((n) => n.node === scope.node) }
			: capData;
		return headroom(c, nodesRes.failed ? null : nodesRes.data, vms, inventory.options);
	});

	// The stat line counts the same VMs the VMs tab lists, and "with issues"
	// reads the same plane as the header. "In a change" is staged or in an
	// open PR, the same set as the VMs tab's chip.
	$effect(() => {
		inventory.proposalsKey;
		untrack(() => reviewCache.loadProposals(inventory.proposals));
	});
	const inPRs = $derived(reviewCache.vmPRs(inventory.proposals));
	const stats = $derived.by(() => {
		const issueKeys = new Set(inventory.issues.map((i) => i.scope));
		const phases = new Map<string, number>();
		let issues = 0;
		let staged = 0;
		for (const vm of vms) {
			if (issueKeys.has(vmKey(vm))) issues++;
			if (drafts.stagedByKey.has(vmKey(vm)) || inPRs.has(vmKey(vm))) staged++;
			const p = vm.paused ? 'Paused' : (vm.phase ?? (vm.power === 'Off' ? 'Stopped' : ''));
			if (p) phases.set(p, (phases.get(p) ?? 0) + 1);
		}
		const order = ['Running', 'Paused', 'Stopped'];
		return {
			issues,
			staged,
			phases: [...phases].sort(
				([a], [b]) => (order.indexOf(a) + 1 || 9) - (order.indexOf(b) + 1 || 9),
			),
		};
	});
	const phaseDot = (p: string) =>
		p === 'Running'
			? TONE_DOT.ok
			: p === 'Paused'
				? TONE_DOT.warn
				: p === 'Stopped'
					? 'border border-ink-faint'
					: /Err|CrashLoop|Unschedulable|Failed/.test(p)
						? TONE_DOT.danger
						: TONE_DOT.info;
</script>

<div class="border-b border-line px-4 py-3">
	<div class="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-sm">
		{#if total}
			<span class="text-2xl font-semibold text-ink"
				>{vms.length} VM{vms.length === 1 ? '' : 's'}</span
			>
		{/if}
		{#if stats.issues}
			<span class="flex items-center gap-1.5 font-medium text-danger-ink">
				<span class="inline-block h-2 w-2 rounded-full {TONE_DOT.danger}"
				></span>{`${stats.issues} with issues`}
			</span>
		{/if}
		{#each stats.phases as [p, n] (p)}
			<span class="flex items-center gap-1.5 text-ink-soft">
				<span class="inline-block h-2 w-2 rounded-full {phaseDot(p)}"
				></span>{`${n} ${p.toLowerCase()}`}
			</span>
		{/each}
		{#if stats.staged}
			<a href="?tab=vms" class="flex items-center gap-1.5 text-accent-ink hover:underline"
				><GitPullRequest size={13} />{`${stats.staged} in a change`}</a
			>
		{/if}
	</div>

	<!-- Quota-aware capacity: ResourceQuota bars at project/namespace scope -
	     the tenant's real boundary, where node-allocatable is the cluster's. -->
	{#if scope.project || scope.namespace}
		<div class="mt-3">
			<QuotaBand scope={{ project: scope.project, namespace: scope.namespace }} />
		</div>
	{/if}
</div>

<div class="grid items-start gap-4 p-4 lg:grid-cols-2">
	<IssuesCard
		scope={{ project: scope.project, namespace: scope.namespace }}
		label={scope.namespace ?? scope.project ?? scope.node ?? 'All VMs'}
	/>
	<CapacityCard {data} loading={summary.loading} failed={summary.failed} {room} cap={capData} />
	{#if data && !metricsGap(data.unavailable)}
		<TopConsumers topCpu={data.topCpu} topMemory={data.topMemory} {vms} {onselect} />
	{/if}
	<ScopeChanges scope={{ project: scope.project, namespace: scope.namespace }} />
</div>
