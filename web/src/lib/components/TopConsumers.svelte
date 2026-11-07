<script lang="ts">
	import type { ConsumerVM, VM } from '#lib/api.js';
	import { bytes, cores, quantity, quantityBytes } from '#lib/format.js';
	import { vmSizing } from '#lib/sizing.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import InfoCard from './InfoCard.svelte';

	// Top VM consumers as a card: CPU-ranked rows with usage bars scaled to the
	// heaviest VM, memory alongside so one glance answers "who is eating what".
	let {
		topCpu,
		topMemory,
		vms = [],
		onselect,
	}: {
		topCpu: ConsumerVM[];
		topMemory: ConsumerVM[];
		// Sizes each row against what the VM was given, not just raw use.
		vms?: VM[];
		onselect?: (namespace: string, name: string) => void;
	} = $props();

	const sizeOf = (c: ConsumerVM) => {
		const vm = vms.find((v) => v.namespace === c.namespace && v.name === c.name);
		return vm ? vmSizing(vm, inventory.options) : {};
	};
	const cpuText = (c: ConsumerVM) => {
		const n = sizeOf(c).cpu;
		return n ? `${Math.round((c.value / n) * 100)}% of ${n} vCPU` : `${cores(c.value)} cores`;
	};
	const memText = (c: ConsumerVM) => {
		const used = topMemory.find((x) => x.name === c.name && x.namespace === c.namespace)?.value;
		if (used === undefined) return '-';
		const m = sizeOf(c).memory;
		if (!m || !(quantityBytes(m) > 0)) return bytes(used);
		const [n, unit] = quantity(m).split(' ');
		const scale = quantityBytes(m) / Number(n);
		return `${(used / scale).toFixed(1)} of ${n} ${unit}`;
	};

	const rows = $derived(topCpu.slice(0, 5));
	const maxCpu = $derived(Math.max(1e-9, ...rows.map((c) => c.value)));
	const memOf = (c: ConsumerVM) =>
		topMemory.find((m) => m.name === c.name && m.namespace === c.namespace)?.value ?? 0;
	const maxMem = $derived(Math.max(1, ...rows.map(memOf)));
</script>

<InfoCard title="Top consumers">
	<div
		class="grid grid-cols-[minmax(8rem,1.4fr)_1fr_1fr] gap-3 border-b border-line-soft px-3 py-1 text-[11px] text-ink-muted"
	>
		<span>VM</span><span>CPU</span><span>Memory</span>
	</div>
	<ul class="divide-y divide-line-soft">
		{#each rows as c (c.namespace + '/' + c.name)}
			<li
				class="grid grid-cols-[minmax(8rem,1.4fr)_1fr_1fr] items-center gap-3 px-3 py-1.5 text-xs"
			>
				<div class="min-w-0">
					<button
						onclick={() => onselect?.(c.namespace, c.name)}
						class="block max-w-full truncate text-ink-soft hover:text-accent-ink hover:underline"
						>{c.name}</button
					>
					<div class="truncate text-[11px] text-ink-faint">{c.namespace}</div>
				</div>
				<div class="flex items-center gap-2">
					<span class="h-1.5 flex-1 overflow-hidden rounded bg-inset-strong">
						<span
							class="block h-full rounded-full bg-accent"
							style="width: {(c.value / maxCpu) * 100}%"
						></span>
					</span>
					<span class="w-24 shrink-0 text-right text-ink-muted tabular-nums">{cpuText(c)}</span>
				</div>
				<div class="flex items-center gap-2">
					<span class="h-1.5 flex-1 overflow-hidden rounded bg-inset-strong">
						<span
							class="block h-full rounded"
							style="width: {(memOf(c) / maxMem) * 100}%; background: var(--chart-2)"
						></span>
					</span>
					<span class="w-24 shrink-0 text-right text-ink-muted tabular-nums">{memText(c)}</span>
				</div>
			</li>
		{:else}
			<li class="px-3 py-2 text-xs text-ink-faint">No usage data.</li>
		{/each}
	</ul>
</InfoCard>
