<script lang="ts">
	import type { VM } from '#lib/api.js';
	import { bytes, cores, plural } from '#lib/format.js';
	import { hostState, type HostLabel } from '#lib/headline.js';
	import { metricsGap } from '#lib/metrics.js';
	import { hrefForScope } from '#lib/nav.js';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';
	import type { HostFacts } from '#lib/state/hosts.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { TONE_DOT, type Tone } from '#lib/status.js';
	import Card from '#lib/components/Card.svelte';
	import CountTile from '#lib/components/CountTile.svelte';
	import HostBalance from '#lib/components/HostBalance.svelte';
	import MetricsGap from '#lib/components/MetricsGap.svelte';
	import Pager from '#lib/components/Pager.svelte';

	// The Hosts root's Summary: the hosts that need a person, then the fleet as
	// one table - state, placement, live utilization, and what is committed to
	// VMs against what the host can give - then the rebalancing strip. Each
	// feed absents its columns when the caller's token can't read it (nodes) or
	// metrics are off; VM placement alone still yields a row per host, like the
	// tree. facts are the page's own reads, shared with its header.
	let { vms, facts }: { vms: VM[]; facts: HostFacts } = $props();

	const nodes = $derived(facts.nodes.failed ? null : facts.nodes.data);
	// A metrics read with no numbers drops its columns, and one line says why.
	const shown = <T extends { unavailable?: string }>(r: { failed: boolean; data: T | null }) =>
		r.failed || !r.data || metricsGap(r.data.unavailable) ? null : r.data;
	const workers = $derived(new Map((shown(facts.load)?.nodes ?? []).map((n) => [n.node, n])));
	const capacity = $derived(new Map((shown(facts.cap)?.nodes ?? []).map((n) => [n.node, n])));
	const reasons = $derived(
		[facts.load.data?.unavailable, facts.cap.data?.unavailable].filter(Boolean),
	);
	const metricsReason = $derived(reasons.find((r) => metricsGap(r)) ?? reasons[0]);
	const placed = $derived.by(() => {
		const m = new Map<string, number>();
		for (const vm of vms) if (vm.nodeName) m.set(vm.nodeName, (m.get(vm.nodeName) ?? 0) + 1);
		return m;
	});
	const unscheduled = $derived(vms.filter((v) => !v.nodeName).length);

	type HostRow = {
		name: string;
		state: { tone: Tone; label: HostLabel } | null;
		vms: number;
		cpuPct?: number;
		memPct?: number;
		vcpu: { used: number; total: number } | null;
		mem: { used: number; total: number } | null;
	};
	const rows = $derived.by((): HostRow[] => {
		const names = new Set<string>([
			...(nodes ?? []).map((n) => n.name),
			...workers.keys(),
			...capacity.keys(),
			...placed.keys(),
		]);
		return [...names].sort().map((name): HostRow => {
			const node = nodes?.find((n) => n.name === name);
			const w = workers.get(name);
			const c = capacity.get(name);
			return {
				name,
				// Without the node list only the load feed's cordon is known.
				state: node
					? hostState(node)
					: w?.unschedulable
						? { tone: 'warn', label: 'Cordoned' }
						: null,
				vms: placed.get(name) ?? 0,
				cpuPct: w?.pct,
				memPct: w?.mem,
				vcpu: c ? { used: c.vcpuAllocated ?? 0, total: c.cpuAllocatable } : null,
				mem: c ? { used: c.memAllocated ?? 0, total: c.memAllocatable } : null,
			};
		});
	});
	const ratio = (m: { used: number; total: number }) => (m.total > 0 ? m.used / m.total : 0);
	const pct = (r: number) => Math.min(100, Math.max(0, r * 100));
	// Placement and commitments are VMs': without KubeVirt every row would read
	// zero, so their columns go. An unknown state keeps them.
	const vmCols = $derived(!vmsOff());
	const hasLoad = $derived(workers.size > 0);
	const hasCap = $derived(vmCols && capacity.size > 0);
	const loading = $derived(facts.nodes.loading || facts.load.loading || facts.cap.loading);

	// The numbers that are problems, each with the hosts behind it.
	const notReady = $derived(rows.filter((r) => r.state?.label === 'Not ready'));
	const draining = $derived(rows.filter((r) => r.state?.label === 'Maintenance' && r.vms > 0));
	const memHot = $derived(rows.filter((r) => (r.memPct ?? 0) > 90));
	const tiles = $derived(notReady.length + draining.length + memHot.length > 0);

	let pg = $state(1);
	let size = $state(DEFAULT_PAGE_SIZE);
	const slice = $derived(pageSlice(rows.length, pg, size));
	const pageRows = $derived(rows.slice(slice.start, slice.end));
	const paged = $derived(rows.length > Math.min(size, DEFAULT_PAGE_SIZE));

	const TH =
		'sticky top-0 z-[1] border-b border-line bg-panel/90 px-2 py-[9px] text-left text-xs font-bold whitespace-nowrap text-ink-muted backdrop-blur';
	const TD = 'h-10 border-b border-line-soft px-2';
</script>

{#snippet bar(fill: number, color: string)}
	<div class="mt-1 h-1.5 max-w-56 overflow-hidden rounded-full bg-[var(--chart-track)]">
		<div class="h-full rounded-full" style="width:{pct(fill)}%;background:{color}"></div>
	</div>
{/snippet}

{#snippet hostLinks(list: HostRow[], lead: string)}
	{lead}
	{#each list.slice(0, 3) as r, i (r.name)}{i ? ', ' : ' '}<a
			href={hrefForScope({ kind: 'node', node: r.name })}
			class="font-mono text-accent-ink underline underline-offset-2">{r.name}</a
		>{/each}{list.length > 3 ? ` and ${list.length - 3} more` : ''}.
{/snippet}

<div class="flex flex-col gap-4">
	{#if tiles}
		<div class="grid grid-cols-[repeat(auto-fit,minmax(min(240px,100%),1fr))] gap-3">
			{#if notReady.length}
				<CountTile
					value={notReady.length}
					label={notReady.length === 1 ? 'host not ready' : 'hosts not ready'}
					tone="danger"
				>
					{@render hostLinks(
						notReady,
						vmCols ? 'VMs cannot start there and new VMs skip them:' : 'New workloads skip them:',
					)}
				</CountTile>
			{/if}
			{#if draining.length}
				<CountTile
					value={draining.length}
					label={draining.length === 1 ? 'host still draining' : 'hosts still draining'}
					tone="warn"
				>
					{@render hostLinks(
						draining,
						'In maintenance with VMs still on them; retry the evacuation on',
					)}
				</CountTile>
			{/if}
			{#if memHot.length}
				<CountTile
					value={memHot.length}
					label={memHot.length === 1 ? 'host above 90% memory' : 'hosts above 90% memory'}
					tone="warn"
				>
					{@render hostLinks(
						memHot,
						vmCols
							? 'New VMs may not fit; rebalancing or a live migration frees room:'
							: 'New workloads may not fit:',
					)}
				</CountTile>
			{/if}
		</div>
	{/if}

	<Card title="All hosts" meta={rows.length ? plural(rows.length, 'host') : undefined} flush>
		{#if rows.length}
			<table class="w-full border-collapse text-[13px] tabular-nums">
				<thead>
					<tr>
						<th scope="col" class="{TH} w-0 pl-card">Host</th>
						<th scope="col" class="{TH} w-0 {vmCols || hasLoad ? '' : 'pr-card'}">State</th>
						{#if vmCols}
							<th scope="col" class="{TH} w-0 pr-6 text-right {hasLoad || hasCap ? '' : 'pr-card'}"
								>VMs</th
							>
						{/if}
						{#if hasLoad}
							<th scope="col" class={TH}>CPU</th>
							<th scope="col" class="{TH} {hasCap ? '' : 'pr-card'}">Memory</th>
						{/if}
						{#if hasCap}
							<th scope="col" class={TH} title="vCPUs committed to VMs vs allocatable">
								vCPU committed
							</th>
							<th scope="col" class="{TH} pr-card" title="Memory committed to guests vs allocatable"
								>Memory committed</th
							>
						{/if}
					</tr>
				</thead>
				<tbody>
					{#each pageRows as r (r.name)}
						<tr class="hover:bg-select-soft">
							<td class="{TD} pl-card whitespace-nowrap">
								<a
									href={hrefForScope({ kind: 'node', node: r.name })}
									class="font-mono font-medium text-ink hover:text-accent-ink">{r.name}</a
								>
							</td>
							<td class="{TD} whitespace-nowrap {vmCols || hasLoad ? '' : 'pr-card'}">
								{#if r.state}
									<span class="inline-flex items-center gap-1.5 text-ink-soft">
										<span class="size-2 rounded-full {TONE_DOT[r.state.tone]}"></span>{r.state
											.label}
									</span>
								{:else}
									<span class="text-ink-muted">-</span>
								{/if}
							</td>
							{#if vmCols}
								<td
									class="{TD} pr-6 text-right whitespace-nowrap text-ink-soft {hasLoad || hasCap
										? ''
										: 'pr-card'}">{r.vms.toLocaleString('en-US')}</td
								>
							{/if}
							{#if hasLoad}
								<td class={TD}>
									{#if r.cpuPct !== undefined}
										<span class="text-xs text-ink-muted">{Math.round(r.cpuPct)}%</span>
										{@render bar(r.cpuPct / 100, 'var(--chart-1)')}
									{/if}
								</td>
								<td class="{TD} {hasCap ? '' : 'pr-card'}">
									{#if r.memPct}
										<span
											class="text-xs {r.memPct > 90
												? 'font-medium text-warn-ink'
												: 'text-ink-muted'}">{Math.round(r.memPct)}%</span
										>
										{@render bar(
											r.memPct / 100,
											r.memPct > 90 ? 'var(--color-warn)' : 'var(--chart-2)',
										)}
									{/if}
								</td>
							{/if}
							{#if hasCap}
								<td class={TD}>
									{#if r.vcpu}
										{@const rc = ratio(r.vcpu)}
										<div class="flex max-w-56 items-baseline justify-between text-xs">
											<span class="text-ink-muted"
												>{cores(r.vcpu.used)} of {cores(r.vcpu.total)}</span
											>
											<span class="text-ink-muted">{rc.toFixed(1)}:1</span>
										</div>
										{@render bar(rc, 'var(--chart-1)')}
									{/if}
								</td>
								<td class="{TD} pr-card">
									{#if r.mem}
										{@const rm = ratio(r.mem)}
										<div class="flex max-w-56 items-baseline justify-between text-xs">
											<span class="text-ink-muted">{bytes(r.mem.used)} of {bytes(r.mem.total)}</span
											>
											<span class={rm > 1 ? 'font-medium text-warn-ink' : 'text-ink-muted'}
												>{rm.toFixed(1)}:1</span
											>
										</div>
										{@render bar(rm, rm > 1 ? 'var(--color-warn)' : 'var(--chart-2)')}
									{/if}
								</td>
							{/if}
						</tr>
					{/each}
				</tbody>
			</table>
			{#if unscheduled > 0}
				<p class="border-b border-line-soft px-card py-2.5 text-[13px] text-ink-muted">
					<a
						href={hrefForScope({ kind: 'node', node: '(unscheduled)' })}
						class="text-accent-ink hover:underline"
					>
						{plural(unscheduled, 'VM')} not placed on any host
					</a>
				</p>
			{/if}
			{#if metricsReason}
				<div class="border-b border-line-soft px-card py-2.5">
					<MetricsGap reason={metricsReason} />
				</div>
			{/if}
			{#if paged}
				<Pager class="px-card py-3" total={rows.length} bind:page={pg} bind:size spread={false} />
			{/if}
		{:else if loading}
			<p class="px-card py-6 text-center text-[13px] text-ink-muted">Loading hosts...</p>
		{:else}
			<p class="px-card py-6 text-center text-[13px] text-ink-muted">
				{vmCols
					? 'No hosts visible. Listing hosts needs node read access; VMs still group by placement in the tree.'
					: 'No hosts visible. Listing hosts needs node read access.'}
			</p>
		{/if}
	</Card>

	<HostBalance load={facts.load} />
</div>
