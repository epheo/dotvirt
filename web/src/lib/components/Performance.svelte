<script lang="ts">
	import { api, METRIC_RANGES, Unauthorized, type VM, type VMMetrics } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import UPlotChart from './UPlotChart.svelte';

	let { vm, onunauthorized }: { vm: VM; onunauthorized?: () => void } = $props();

	// Mirrors vCenter's real-time / historical tiers.
	const RANGES = METRIC_RANGES;
	let range = $state('1h');
	let metrics = $state<VMMetrics | null>(null);
	let loading = $state(false);
	let error = $state('');

	async function load() {
		loading = true;
		error = '';
		try {
			metrics = await api.metrics(vm.namespace, vm.name, range);
		} catch (e) {
			if (e instanceof Unauthorized) {
				onunauthorized?.();
				return;
			}
			error = String(e);
			metrics = null;
		} finally {
			loading = false;
		}
	}

	// Reload when the VM or range changes.
	$effect(() => {
		vm.namespace;
		vm.name;
		range;
		load();
	});

	// Auto-refresh in real-time mode (vCenter's ~20s; we use 30s to match the step),
	// paused while the tab is backgrounded.
	$effect(() => {
		if (range !== '1h') return;
		return pollWhileVisible(load, 30000);
	});
</script>

<div class="space-y-3">
	<div class="flex items-center gap-2">
		<span class="text-xs font-medium text-slate-500">Range</span>
		{#each RANGES as r (r.key)}
			<button
				onclick={() => (range = r.key)}
				class="rounded border px-2 py-0.5 text-xs {range === r.key
					? 'border-blue-500 bg-blue-50 text-blue-700'
					: 'border-slate-300 text-slate-600 hover:bg-slate-50'}">{r.label}</button
			>
		{/each}
		{#if loading}<span class="text-xs text-slate-400">updating…</span>{/if}
	</div>

	{#if error}
		<p class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</p>
	{:else if metrics}
		<div class="grid grid-cols-1 gap-3 xl:grid-cols-2">
			{#each metrics.charts as chart (chart.key)}
				<UPlotChart {chart} />
			{/each}
		</div>
	{:else}
		<p class="py-8 text-center text-sm text-slate-400">Loading metrics…</p>
	{/if}
</div>
