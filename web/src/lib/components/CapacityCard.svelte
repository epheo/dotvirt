<script lang="ts">
	import type { ClusterSummary, HostCapacity } from '#lib/api.js';
	import { bytes, quantity } from '#lib/format.js';
	import type { Headroom } from '#lib/headroom.js';
	import Button from './Button.svelte';
	import InfoCard from './InfoCard.svelte';

	// Can I add a VM here: the headroom answer first, then each resource as one
	// bar in real units. The solid band is in use now, the light band what VMs
	// have requested, the hatched end the capacity of hosts that take no VMs.
	let {
		data,
		loading,
		failed,
		room,
		cap,
	}: {
		data: ClusterSummary | null;
		loading: boolean;
		failed: boolean;
		room: Headroom | null;
		cap: HostCapacity | null;
	} = $props();

	const pct = (v: number, total: number) => (total > 0 ? Math.min(100, (v / total) * 100) : 0);
	const trim = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));

	// Capacity of the hosts left out of the headroom count, as a share of each total.
	const outside = $derived.by(() => {
		const names = new Set((room?.excluded ?? []).map((x) => x.node));
		let cpu = 0;
		let mem = 0;
		for (const n of cap?.nodes ?? [])
			if (names.has(n.node)) {
				cpu += n.cpuAllocatable;
				mem += n.memAllocatable;
			}
		return { cpu, mem };
	});

	type Bar = { label: string; value: string; used: number; held: number; out: number };
	const bars = $derived.by((): Bar[] => {
		if (!data) return [];
		return [
			{
				label: 'CPU',
				value: data.cpu.total
					? `${trim(data.cpu.used)} of ${trim(data.cpu.total)} cores in use`
					: 'no data',
				used: pct(data.cpu.used, data.cpu.total),
				held: pct(data.cpu.allocated ?? 0, data.cpu.total),
				out: pct(outside.cpu, data.cpu.total),
			},
			{
				label: 'Memory',
				value: data.memory.total
					? `${bytes(data.memory.used)} of ${bytes(data.memory.total)} in use`
					: 'no data',
				used: pct(data.memory.used, data.memory.total),
				held: pct(data.memory.allocated ?? 0, data.memory.total),
				out: pct(outside.mem, data.memory.total),
			},
			{
				label: 'Guest disks',
				value: data.storage.total
					? `${bytes(data.storage.used)} of ${bytes(data.storage.total)} used`
					: 'no data',
				used: pct(data.storage.used, data.storage.total),
				held: 0,
				out: 0,
			},
		];
	});

	const requested = $derived(
		data && (data.cpu.allocated || data.memory.allocated)
			? `${trim(data.cpu.allocated ?? 0)} vCPU, ${bytes(data.memory.allocated ?? 0)}`
			: '',
	);
	const HATCH =
		'repeating-linear-gradient(135deg, var(--color-line-strong) 0 1.5px, transparent 1.5px 5px)';
</script>

<InfoCard title="Capacity">
	{#snippet action()}
		<Button variant="link" size="sm" href="/hosts">See hosts</Button>
	{/snippet}
	<div class="space-y-4 px-card py-3">
		{#if room}
			<div>
				<p class="text-xl font-semibold text-balance text-ink">
					Room for {room.count} more {room.size.name ? `${room.size.name} ` : ''}VM{room.count === 1
						? ''
						: 's'}
				</p>
				<p class="mt-1 text-xs text-ink-soft">
					{room.size.cpu} vCPU and {quantity(room.size.memory)} each. Memory is the limit; vCPU is shared,
					{room.vcpuPerCore.toFixed(1)} committed per core.{#each room.excluded as x (x.node)}
						{` ${x.node} is ${x.reason} and not counted.`}{/each}
				</p>
			</div>
		{/if}

		{#if data}
			<div class="space-y-3">
				{#each bars as b (b.label)}
					<div>
						<div class="flex items-baseline justify-between gap-3 text-[13px]">
							<span class="font-semibold text-ink">{b.label}</span>
							<span class="text-ink-soft tabular-nums">{b.value}</span>
						</div>
						<div class="relative mt-1.5 h-2.5 overflow-hidden rounded-sm bg-[var(--chart-track)]">
							<span
								class="absolute inset-y-0 left-0 bg-[var(--chart-1)] opacity-35"
								style:width="{b.held}%"
							></span>
							<span class="absolute inset-y-0 left-0 bg-[var(--chart-1)]" style:width="{b.used}%"
							></span>
							{#if b.out > 0}
								<span
									class="absolute inset-y-0 right-0"
									style:width="{b.out}%"
									style:background={HATCH}
								></span>
							{/if}
						</div>
					</div>
				{/each}
			</div>
			<p class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-ink-muted">
				<span class="flex items-center gap-1.5"
					><span class="size-2.5 rounded-sm bg-[var(--chart-1)]"></span>In use now</span
				>
				{#if requested}
					<span class="flex items-center gap-1.5"
						><span class="size-2.5 rounded-sm bg-[var(--chart-1)] opacity-35"></span>Requested by
						VMs ({requested})</span
					>
				{/if}
				{#each room?.excluded ?? [] as x (x.node)}
					<span class="flex items-center gap-1.5"
						><span class="size-2.5 rounded-sm border border-line-strong" style:background={HATCH}
						></span>{x.node}, {x.reason}</span
					>
				{/each}
			</p>
		{:else if loading}
			<p class="text-xs text-ink-faint">Loading capacity...</p>
		{:else if failed}
			<p class="text-xs text-ink-muted">
				Capacity unavailable: cluster metrics are off or unreachable.
			</p>
		{/if}
	</div>
</InfoCard>
