<script lang="ts">
	import { METRIC_RANGES, type VMMetrics } from '$lib/api';
	import { resource } from '$lib/resource.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import UPlotChart from './UPlotChart.svelte';

	// The one Performance panel - range tiers spanning real-time /
	// historical tiers, 30s real-time refresh (paused while the tab is
	// backgrounded), and the chart grid. The caller supplies the query and
	// {#key}s this component on the queried identity (VM or scope), so a new
	// target remounts with a fresh range.
	let {
		load,
		emptyText = '',
	}: {
		load: (range: string) => Promise<VMMetrics>;
		// Non-empty: shown when the query succeeds but every chart came back
		// empty (e.g. a scope with no VM samples yet).
		emptyText?: string;
	} = $props();

	const RANGES = METRIC_RANGES;
	let range = $state('1h');
	// Keyed on the range (a new range is a new query, so the old charts blank);
	// only real-time mode auto-refreshes, at the 30s step.
	const res = resource<VMMetrics>(
		() => range,
		(r) => load(r),
		{ poll: () => (range === '1h' ? 30000 : 0), reset: true },
	);
	const metrics = $derived(res.data);

	const empty = $derived(
		!!emptyText && !!metrics && metrics.charts.every((c) => c.series.length === 0),
	);
</script>

<div class="space-y-3">
	<div class="flex items-center gap-2">
		<span class="text-xs font-medium text-ink-muted">Range</span>
		{#each RANGES as r (r.key)}
			<button
				onclick={() => (range = r.key)}
				class="rounded border px-2 py-0.5 text-xs {range === r.key
					? 'border-accent bg-select-soft text-accent-ink'
					: 'border-line-strong text-ink-soft hover:bg-inset'}">{r.label}</button
			>
		{/each}
	</div>

	{#if res.failed}
		<ErrorNote error={res.error} />
	{:else if empty}
		<p class="py-8 text-center text-sm text-ink-faint">{emptyText}</p>
	{:else if metrics}
		<div class="grid grid-cols-1 gap-3 xl:grid-cols-2">
			{#each metrics.charts as chart (chart.key)}
				<UPlotChart {chart} />
			{/each}
		</div>
	{:else}
		<p class="py-8 text-center text-sm text-ink-faint">Loading metrics…</p>
	{/if}
</div>
