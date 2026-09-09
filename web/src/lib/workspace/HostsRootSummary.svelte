<script lang="ts">
	import { api, type HostCapacity, type HostLoad, type Node, type VM } from '$lib/api';
	import { bytes, cores } from '$lib/format';
	import { hrefForScope } from '$lib/nav';
	import { resource } from '$lib/resource.svelte';
	import type { Tone } from '$lib/status';
	import HostBalance from '$lib/components/HostBalance.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';

	// The Hosts root's Summary: the fleet as one table - state, placement,
	// live utilization, and what is committed to VMs against what the node can
	// give - then the DRS balance strip. Each feed absents its columns when the
	// caller's token can't read it (nodes) or metrics are off; VM placement
	// alone still yields a row per host, like the tree.
	let { vms }: { vms: VM[] } = $props();

	const nodesRes = resource<Node[]>(() => 'nodes', api.nodes);
	const load = resource<HostLoad>(
		() => '',
		() => api.hostLoad(),
		{ poll: 30000 },
	);
	const cap = resource<HostCapacity>(
		() => '',
		() => api.capacity(),
		{ poll: 30000 },
	);
	const nodes = $derived(nodesRes.failed ? null : nodesRes.data);
	const workers = $derived(
		new Map((load.failed ? [] : (load.data?.nodes ?? [])).map((n) => [n.node, n])),
	);
	const capacity = $derived(
		new Map((cap.failed ? [] : (cap.data?.nodes ?? [])).map((n) => [n.node, n])),
	);
	const placed = $derived.by(() => {
		const m = new Map<string, number>();
		for (const vm of vms) if (vm.nodeName) m.set(vm.nodeName, (m.get(vm.nodeName) ?? 0) + 1);
		return m;
	});
	const unscheduled = $derived(vms.filter((v) => !v.nodeName).length);

	const rows = $derived.by(() => {
		const names = new Set<string>([
			...(nodes ?? []).map((n) => n.name),
			...workers.keys(),
			...capacity.keys(),
			...placed.keys(),
		]);
		return [...names].sort().map((name) => {
			const node = nodes?.find((n) => n.name === name);
			const w = workers.get(name);
			const c = capacity.get(name);
			return {
				name,
				state: stateOf(node, w?.unschedulable),
				vms: placed.get(name) ?? 0,
				cpuPct: w?.pct,
				memPct: w?.mem,
				vcpu: c ? { used: c.vcpuAllocated ?? 0, total: c.cpuAllocatable } : null,
				mem: c ? { used: c.memAllocated ?? 0, total: c.memAllocatable } : null,
			};
		});
	});

	// Maintenance is the intent marker and outranks the plain cordon it implies.
	function stateOf(
		node: Node | undefined,
		cordoned?: boolean,
	): { tone: Tone; label: string } | null {
		if (!node) return cordoned ? { tone: 'warn', label: 'Cordoned' } : null;
		if (node.maintenance) return { tone: 'warn', label: 'Maintenance' };
		if (!node.ready) return { tone: 'danger', label: 'Not ready' };
		if (node.unschedulable) return { tone: 'warn', label: 'Cordoned' };
		return { tone: 'ok', label: 'Ready' };
	}
	const ratio = (m: { used: number; total: number }) => (m.total > 0 ? m.used / m.total : 0);
	const pct = (r: number) => Math.min(100, Math.max(0, r * 100));
	const hasLoad = $derived(workers.size > 0);
	const hasCap = $derived(capacity.size > 0);
	const loading = $derived(nodesRes.loading || load.loading || cap.loading);
</script>

{#snippet bar(fill: number, color: string)}
	<div
		class="mt-0.5 h-1.5 max-w-56 overflow-hidden rounded-full"
		style="background:var(--chart-track)"
	>
		<div class="h-full rounded-full" style="width:{pct(fill)}%;background:{color}"></div>
	</div>
{/snippet}

<!-- svelte-ignore a11y_no_noninteractive_tabindex (axe scrollable-region-focusable: a scroll region must be keyboard-reachable) -->
<div class="min-h-0 flex-1 overflow-y-auto" role="region" aria-label="Tab content" tabindex="0">
	{#if rows.length}
		<table class="w-full text-left text-[13px]">
			<thead class="border-b border-line text-xs text-ink-muted">
				<tr>
					<th class="w-0 px-4 py-2 font-medium whitespace-nowrap">Host</th>
					<th class="w-0 px-4 py-2 font-medium whitespace-nowrap">State</th>
					<th class="w-0 px-4 py-2 text-right font-medium whitespace-nowrap">VMs</th>
					{#if hasLoad}
						<th class="px-4 py-2 font-medium">CPU</th>
						<th class="px-4 py-2 font-medium">Memory</th>
					{/if}
					{#if hasCap}
						<th class="px-4 py-2 font-medium" title="vCPUs committed to VMs vs allocatable">
							vCPU committed
						</th>
						<th class="px-4 py-2 font-medium" title="Memory committed to guests vs allocatable">
							Memory committed
						</th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each rows as r (r.name)}
					<tr class="border-b border-line-soft hover:bg-select-soft">
						<td class="px-4 py-1.5 whitespace-nowrap">
							<a
								href={hrefForScope({ kind: 'node', node: r.name })}
								class="font-medium text-ink hover:text-accent-ink">{r.name}</a
							>
						</td>
						<td class="px-4 py-1.5 whitespace-nowrap">
							{#if r.state}
								<StatusPill tone={r.state.tone} label={r.state.label} />
							{:else}
								<span class="text-ink-faint">—</span>
							{/if}
						</td>
						<td class="px-4 py-1.5 text-right whitespace-nowrap text-ink-soft">{r.vms}</td>
						{#if hasLoad}
							<td class="px-4 py-1.5">
								{#if r.cpuPct !== undefined}
									<span class="text-[11px] text-ink-muted">{Math.round(r.cpuPct)}%</span>
									{@render bar(r.cpuPct / 100, 'var(--chart-1)')}
								{/if}
							</td>
							<td class="px-4 py-1.5">
								{#if r.memPct}
									<span
										class="text-[11px] {r.memPct > 90
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
							<td class="px-4 py-1.5">
								{#if r.vcpu}
									{@const rc = ratio(r.vcpu)}
									<div class="flex max-w-56 items-baseline justify-between text-[11px]">
										<span class="text-ink-muted">{cores(r.vcpu.used)} / {cores(r.vcpu.total)}</span>
										<span class="text-ink-faint">{rc.toFixed(1)}:1</span>
									</div>
									{@render bar(rc, 'var(--chart-1)')}
								{/if}
							</td>
							<td class="px-4 py-1.5">
								{#if r.mem}
									{@const rm = ratio(r.mem)}
									<div class="flex max-w-56 items-baseline justify-between text-[11px]">
										<span class="text-ink-muted">{bytes(r.mem.used)} / {bytes(r.mem.total)}</span>
										<span class={rm > 1 ? 'font-medium text-warn-ink' : 'text-ink-faint'}
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
			<p class="border-b border-line-soft px-4 py-1.5 text-xs text-ink-faint">
				<a href={hrefForScope({ kind: 'node', node: '(unscheduled)' })} class="hover:underline">
					{unscheduled} VM{unscheduled === 1 ? '' : 's'} not placed on any host
				</a>
			</p>
		{/if}
	{:else if loading}
		<p class="py-6 text-center text-sm text-ink-faint">Loading hosts…</p>
	{:else}
		<p class="py-6 text-center text-sm text-ink-faint">
			No hosts visible. Listing nodes needs node read access; VMs still group by placement.
		</p>
	{/if}

	<div class="p-4">
		<div class="max-w-3xl">
			<HostBalance roster={false} />
		</div>
	</div>
</div>
