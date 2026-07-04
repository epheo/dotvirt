<script lang="ts">
	import { untrack } from 'svelte';
	import { METRIC_RANGES, Unauthorized, type VMMetrics } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import UPlotChart from './UPlotChart.svelte';

	// The one Performance panel — range tiers mirroring vCenter's real-time /
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
	let metrics = $state<VMMetrics | null>(null);
	let loading = $state(false);
	let error = $state('');

	const empty = $derived(
		!!emptyText && !!metrics && metrics.charts.every((c) => c.series.length === 0),
	);

	async function refresh() {
		loading = true;
		error = '';
		try {
			metrics = await load(range);
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally by the api layer
			error = String(e);
			metrics = null;
		} finally {
			loading = false;
		}
	}

	// Reload on range change; untrack the call so the query's own reads (the
	// caller's per-frame vm/scope props) don't re-fire this effect.
	$effect(() => {
		range;
		untrack(refresh);
	});

	// Auto-refresh in real-time mode (vCenter's ~20s; we use 30s to match the step).
	$effect(() => {
		if (range !== '1h') return;
		return pollWhileVisible(refresh, 30000);
	});
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
		{#if loading}<span class="text-xs text-ink-faint">updating…</span>{/if}
	</div>

	{#if error}
		<p class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</p>
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
