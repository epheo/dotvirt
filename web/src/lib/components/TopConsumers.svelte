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
</script>

<InfoCard title="Top consumers">
	<table class="w-full text-[13px]">
		<thead>
			<tr class="border-b border-line text-left text-xs text-ink-muted">
				<th class="w-1/2 py-1.5 pr-3 pl-card font-semibold">VM</th>
				<th class="px-3 py-1.5 font-semibold">CPU</th>
				<th class="py-1.5 pr-card pl-3 font-semibold">Memory</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-line-soft">
			{#each rows as c (c.namespace + '/' + c.name)}
				<tr>
					<td class="max-w-0 truncate py-2 pr-3 pl-card">
						<button
							onclick={() => onselect?.(c.namespace, c.name)}
							class="font-mono text-ink hover:text-accent-ink hover:underline">{c.name}</button
						>
						<span class="ml-1 text-ink-muted">{c.namespace}</span>
					</td>
					<td class="px-3 py-2 whitespace-nowrap text-ink-soft tabular-nums">{cpuText(c)}</td>
					<td class="py-2 pr-card pl-3 whitespace-nowrap text-ink-soft tabular-nums"
						>{memText(c)}</td
					>
				</tr>
			{:else}
				<tr><td colspan="3" class="px-card py-2 text-xs text-ink-faint">No usage data.</td></tr>
			{/each}
		</tbody>
	</table>
</InfoCard>
