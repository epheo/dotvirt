<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import type { MetricChart } from '$lib/api';
	import { bytes } from '$lib/format';

	let { chart }: { chart: MetricChart } = $props();

	let el: HTMLDivElement;
	let width = $state(0);

	const palette = ['#2563eb', '#0d9488', '#f59e0b', '#dc2626', '#7c3aed', '#16a34a'];

	// Format a value for the axis/legend by the chart's unit hint.
	function fmt(unit: string, v: number | null): string {
		if (v == null || Number.isNaN(v)) return '–';
		if (unit === '%') return v.toFixed(1) + '%';
		if (unit === 'ms') return v.toFixed(2) + ' ms';
		if (unit === 'bytes') return bytes(v);
		if (unit === 'Bps') return bytes(v) + '/s';
		if (unit === 'cores') return v >= 0.1 || v === 0 ? v.toFixed(2) : v.toPrecision(2);
		return String(v);
	}

	function makeData(c: MetricChart): uPlot.AlignedData {
		return [c.times, ...c.series.map((s) => s.values)] as unknown as uPlot.AlignedData;
	}

	function makeOpts(c: MetricChart, w: number): uPlot.Options {
		return {
			width: w,
			height: 150,
			padding: [8, 10, 0, 0],
			legend: { show: true, live: true },
			cursor: { points: { size: 6 } },
			scales: { x: { time: true } },
			series: [
				{},
				...c.series.map((s, i) => ({
					label: s.name,
					stroke: palette[i % palette.length],
					width: 1.5,
					points: { show: false },
					value: (_u: uPlot, v: number) => fmt(c.unit, v)
				}))
			],
			axes: [
				{
					stroke: '#94a3b8',
					grid: { stroke: '#f1f5f9', width: 1 },
					ticks: { stroke: '#e2e8f0', size: 4 },
					size: 26,
					font: '11px sans-serif'
				},
				{
					stroke: '#94a3b8',
					grid: { stroke: '#f1f5f9', width: 1 },
					ticks: { stroke: '#e2e8f0', size: 4 },
					size: 64,
					font: '11px sans-serif',
					values: (_u: uPlot, splits: number[]) => splits.map((v) => fmt(c.unit, v))
				}
			]
		};
	}

	// Recreate the plot when its data or the container width changes (gated on a
	// real width so we don't build a zero-width chart on first paint).
	$effect(() => {
		const c = chart;
		const w = width;
		if (!el || w <= 0) return;
		const plot = new uPlot(makeOpts(c, w), makeData(c), el);
		return () => plot.destroy();
	});

	$effect(() => {
		if (!el) return;
		const ro = new ResizeObserver((entries) => {
			width = Math.floor(entries[0].contentRect.width);
		});
		ro.observe(el);
		return () => ro.disconnect();
	});
</script>

<div class="rounded border border-slate-200 bg-white p-2">
	<div class="mb-1 px-1 text-xs font-semibold text-slate-600">{chart.title}</div>
	<div bind:this={el} class="uplot-host"></div>
</div>

<style>
	.uplot-host {
		width: 100%;
	}
	:global(.uplot) {
		font-family: inherit;
	}
	:global(.u-legend) {
		font-size: 11px;
	}
</style>
