<script lang="ts">
	import type { ConsumerVM } from '$lib/api';
	import { bytes, cores } from '$lib/format';
	import InfoCard from './InfoCard.svelte';

	// Top VM consumers as a card: CPU-ranked rows with usage bars scaled to the
	// heaviest VM, memory alongside so one glance answers "who is eating what".
	let {
		topCpu,
		topMemory,
		onselect,
	}: {
		topCpu: ConsumerVM[];
		topMemory: ConsumerVM[];
		onselect?: (namespace: string, name: string) => void;
	} = $props();

	const rows = $derived(topCpu.slice(0, 5));
	const maxCpu = $derived(Math.max(1e-9, ...rows.map((c) => c.value)));
	const memOf = (c: ConsumerVM) =>
		topMemory.find((m) => m.name === c.name && m.namespace === c.namespace)?.value ?? 0;
	const maxMem = $derived(Math.max(1, ...rows.map(memOf)));
</script>

<InfoCard title="Top consumers">
	<ul class="divide-y divide-line-soft">
		{#each rows as c (c.namespace + '/' + c.name)}
			<li
				class="grid grid-cols-[minmax(8rem,1.4fr)_1fr_1fr] items-center gap-3 px-3 py-1.5 text-xs"
			>
				<div class="min-w-0">
					<button
						onclick={() => onselect?.(c.namespace, c.name)}
						class="block max-w-full truncate text-ink-soft hover:text-blue-700 hover:underline"
						>{c.name}</button
					>
					<div class="truncate text-[11px] text-ink-faint">{c.namespace}</div>
				</div>
				<div class="flex items-center gap-2">
					<span class="h-1.5 flex-1 overflow-hidden rounded bg-inset-strong">
						<span
							class="block h-full rounded bg-blue-500"
							style="width: {(c.value / maxCpu) * 100}%"
						></span>
					</span>
					<span class="w-16 shrink-0 text-right text-ink-muted">{cores(c.value)} cores</span>
				</div>
				<div class="flex items-center gap-2">
					<span class="h-1.5 flex-1 overflow-hidden rounded bg-inset-strong">
						<span
							class="block h-full rounded bg-teal-500"
							style="width: {(memOf(c) / maxMem) * 100}%"
						></span>
					</span>
					<span class="w-14 shrink-0 text-right text-ink-muted">{bytes(memOf(c))}</span>
				</div>
			</li>
		{:else}
			<li class="px-3 py-2 text-xs text-ink-faint">No usage data.</li>
		{/each}
	</ul>
</InfoCard>
