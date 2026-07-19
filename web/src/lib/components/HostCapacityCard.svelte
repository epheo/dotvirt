<script lang="ts">
	import { untrack } from 'svelte';
	import { api, Unauthorized, type HostCapacity } from '$lib/api';
	import { bytes } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';

	// Per-host overcommit: the cluster ratios from the summary band, broken
	// down to the workers that carry them. Server-sorted most-committed-memory
	// first — memory (unlike time-shared CPU) is the ratio that hurts. The
	// card absents itself without node-read access, like the balance card.
	let data = $state<HostCapacity | null>(null);

	async function load() {
		try {
			data = await api.capacity();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			data = null;
		}
	}
	$effect(() => {
		untrack(load);
	});
	$effect(() => pollWhileVisible(load, 30000));

	const MAX = 8;
	const ratio = (alloc: number | undefined, total: number) =>
		total > 0 ? (alloc ?? 0) / total : 0;
	const pct = (r: number) => Math.min(100, r * 100);

	const cores = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
</script>

{#if data?.nodes.length}
	<section class="rounded border border-line">
		<h3
			class="flex items-center justify-between border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
		>
			<span>Host capacity</span>
			<span class="font-normal text-ink-faint normal-case">committed to VMs vs allocatable</span>
		</h3>
		<ul class="divide-y divide-line-soft p-1 text-[13px]">
			{#each data.nodes.slice(0, MAX) as n (n.node)}
				{@const rc = ratio(n.vcpuAllocated, n.cpuAllocatable)}
				{@const rm = ratio(n.memAllocated, n.memAllocatable)}
				<li class="grid grid-cols-[minmax(6rem,1fr)_2fr_2fr] items-center gap-3 px-2 py-1.5">
					<a href="/hosts/{n.node}" class="truncate font-medium text-ink hover:text-accent-ink"
						>{n.node}</a
					>
					<div>
						<div class="flex items-baseline justify-between text-[11px]">
							<span class="text-ink-muted"
								>vCPU {cores(n.vcpuAllocated ?? 0)} / {cores(n.cpuAllocatable)}</span
							>
							<span class="text-ink-faint">{rc.toFixed(1)}:1</span>
						</div>
						<div
							class="mt-0.5 h-1.5 overflow-hidden rounded-full"
							style="background:var(--chart-track)"
						>
							<div
								class="h-full rounded-full"
								style="width:{pct(rc)}%;background:var(--chart-1)"
							></div>
						</div>
					</div>
					<div>
						<div class="flex items-baseline justify-between text-[11px]">
							<span class="text-ink-muted"
								>Mem {bytes(n.memAllocated ?? 0)} / {bytes(n.memAllocatable)}</span
							>
							<span class={rm > 1 ? 'font-medium text-warn-ink' : 'text-ink-faint'}
								>{rm.toFixed(1)}:1</span
							>
						</div>
						<div
							class="mt-0.5 h-1.5 overflow-hidden rounded-full"
							style="background:var(--chart-track)"
						>
							<div
								class="h-full rounded-full"
								style="width:{pct(rm)}%;background:{rm > 1
									? 'var(--color-warn)'
									: 'var(--chart-2)'}"
							></div>
						</div>
					</div>
				</li>
			{/each}
		</ul>
		{#if data.nodes.length > MAX}
			<p class="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-ink-faint">
				and {data.nodes.length - MAX} more workers
			</p>
		{/if}
	</section>
{/if}
