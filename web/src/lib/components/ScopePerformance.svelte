<script lang="ts">
	import { api, Unauthorized, type VMMetrics } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import UPlotChart from './UPlotChart.svelte';

	// The container Monitor's Performance view: per-VM top-consumer charts for
	// the current scope (all / project / namespace / node). Mirrors the VM
	// Performance tab's range tiers and refresh cadence.
	let {
		scope,
		onunauthorized
	}: {
		scope: { project?: string; namespace?: string; node?: string };
		onunauthorized?: () => void;
	} = $props();

	const RANGES = [
		{ key: '1h', label: 'Real-time' },
		{ key: '1d', label: 'Day' },
		{ key: '1w', label: 'Week' }
	];
	let range = $state('1h');
	let metrics = $state<VMMetrics | null>(null);
	let loading = $state(false);
	let error = $state('');

	// A stable primitive key for the scope: the parent re-derives the scope
	// object per inventory frame, but its CONTENT only changes on a real scope
	// change — without this the charts would refetch continuously.
	const scopeKey = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}|${scope.node ?? ''}`);

	const empty = $derived(!!metrics && metrics.charts.every((c) => c.series.length === 0));

	async function load() {
		const s = scope;
		loading = true;
		error = '';
		try {
			metrics = await api.scopeMetrics(
				{ project: s.project, namespace: s.namespace, node: s.node },
				range
			);
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

	// Reload when the scope or range changes (keyed on the stable string).
	$effect(() => {
		scopeKey;
		range;
		load();
	});

	// Auto-refresh in real-time mode, paused while the tab is backgrounded.
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
	{:else if empty}
		<p class="py-8 text-center text-sm text-slate-400">No VM metrics in this scope yet.</p>
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
