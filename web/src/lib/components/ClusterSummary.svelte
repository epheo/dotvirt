<script lang="ts">
	import {
		api,
		vmKey,
		type ClusterMetric,
		type ClusterSummary,
		type HostCapacity,
		type Node,
		type VM,
	} from '#lib/api.js';
	import { bytes, quantity } from '#lib/format.js';
	import { headroom } from '#lib/headroom.js';
	import { resource } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { TONE_DOT } from '#lib/status.js';
	import IssuesCard from './IssuesCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import TopConsumers from './TopConsumers.svelte';

	// A scope's Summary answers three questions in order: what runs here and
	// what needs a person (live stream, always), how full the hosts are in real
	// units, and how many more VMs fit (metrics, when present).
	let {
		scope = {},
		vms,
		onselect,
	}: {
		scope?: { project?: string; namespace?: string; node?: string };
		vms: VM[];
		onselect?: (namespace: string, name: string) => void;
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
	const nodesRes = resource<Node[]>(() => 'nodes', api.nodes);
	const room = $derived.by(() => {
		if (cap.failed || !cap.data) return null;
		const c = scope.node
			? { ...cap.data, nodes: cap.data.nodes.filter((n) => n.node === scope.node) }
			: cap.data;
		return headroom(c, nodesRes.failed ? null : nodesRes.data, vms, inventory.options);
	});

	// The stat line counts the same VMs the VMs tab lists, and "with issues"
	// reads the same plane as the header.
	const stats = $derived.by(() => {
		const issueKeys = new Set(inventory.issues.map((i) => i.scope));
		const phases = new Map<string, number>();
		let issues = 0;
		let staged = 0;
		for (const vm of vms) {
			if (issueKeys.has(vmKey(vm))) issues++;
			if (drafts.stagedByKey.has(vmKey(vm))) staged++;
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

	// Each bar: what is in use now, what VMs have committed (the lighter band,
	// clipped at the boundary), against node-allocatable.
	type Bar = { label: string; now: string; committed?: string; used: number; held: number };
	const pct = (v: number, total: number) => (total > 0 ? Math.min(100, (v / total) * 100) : 0);
	const bars = $derived.by((): Bar[] => {
		if (!data) return [];
		const c = (m: ClusterMetric) => ({
			used: pct(m.used, m.total),
			held: pct(m.allocated ?? 0, m.total),
		});
		return [
			{
				label: 'CPU',
				now: data.cpu.total
					? `${trim(data.cpu.used)} of ${trim(data.cpu.total)} cores in use`
					: 'no data',
				committed: data.cpu.allocated ? `${trim(data.cpu.allocated)} vCPU committed` : undefined,
				...c(data.cpu),
			},
			{
				label: 'Memory',
				now: data.memory.total
					? `${bytes(data.memory.used)} of ${bytes(data.memory.total)} in use`
					: 'no data',
				committed: data.memory.allocated ? `${bytes(data.memory.allocated)} committed` : undefined,
				...c(data.memory),
			},
			{
				label: 'Guest disks',
				now: data.storage.total
					? `${bytes(data.storage.used)} of ${bytes(data.storage.total)} used`
					: 'no data',
				used: pct(data.storage.used, data.storage.total),
				held: 0,
			},
		];
	});
	const trim = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
</script>

<div class="border-b border-line bg-inset px-4 py-3">
	<div class="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-sm">
		<span class="text-lg font-semibold text-ink">{vms.length} VM{vms.length === 1 ? '' : 's'}</span>
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
			<span class="text-accent-ink">{stats.staged} staged</span>
		{/if}
	</div>

	{#if data}
		<div class="mt-3 grid gap-x-8 gap-y-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
			<div class="grid gap-3 sm:grid-cols-3">
				{#each bars as b (b.label)}
					<div class="min-w-0">
						<div class="text-xs font-semibold tracking-wide text-ink-muted uppercase">
							{b.label}
						</div>
						<div class="mt-0.5 text-[13px] text-ink tabular-nums">{b.now}</div>
						<div class="relative mt-1.5 h-2 overflow-hidden rounded-sm bg-[var(--chart-track)]">
							<span
								class="absolute inset-y-0 left-0 bg-[var(--chart-1)] opacity-35"
								style:width="{b.held}%"
							></span>
							<span class="absolute inset-y-0 left-0 bg-[var(--chart-1)]" style:width="{b.used}%"
							></span>
						</div>
						{#if b.committed}
							<div class="mt-1 text-xs text-ink-muted tabular-nums">{b.committed}</div>
						{/if}
					</div>
				{/each}
			</div>

			{#if room}
				<div class="min-w-0">
					<div class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Headroom</div>
					<div class="mt-0.5 text-[15px] font-semibold text-ink">
						Room for about {room.count} more VM{room.count === 1 ? '' : 's'}
					</div>
					<p class="mt-0.5 text-xs text-ink-soft">
						Sized like most of yours: {room.size.cpu} vCPU, {quantity(room.size.memory)}{room.size
							.name
							? ` (${room.size.name})`
							: ''}. Memory is the limit; vCPU is shared, {room.vcpuPerCore.toFixed(1)} committed per
						core.
					</p>
					{#each room.excluded as x (x.node)}
						<p class="text-xs text-ink-muted">{x.node} is {x.reason} and not counted.</p>
					{/each}
				</div>
			{/if}
		</div>
	{:else if summary.loading}
		<p class="mt-2 text-xs text-ink-faint">Loading capacity...</p>
	{:else if summary.failed}
		<p class="mt-2 text-xs text-ink-muted">
			Capacity unavailable: cluster metrics are off or unreachable.
		</p>
	{/if}

	<!-- Quota-aware capacity: ResourceQuota bars at project/namespace scope -
	     the tenant's real boundary, where node-allocatable is the cluster's. -->
	{#if scope.project || scope.namespace}
		<div class="mt-3">
			<QuotaBand scope={{ project: scope.project, namespace: scope.namespace }} />
		</div>
	{/if}
</div>

<div class="grid items-start gap-4 p-4 lg:grid-cols-2">
	<IssuesCard scope={{ project: scope.project, namespace: scope.namespace }} />
	{#if data}
		<TopConsumers topCpu={data.topCpu} topMemory={data.topMemory} {onselect} />
	{/if}
</div>
