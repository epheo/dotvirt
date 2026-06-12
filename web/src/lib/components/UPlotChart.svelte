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
		if (unit === 'iops') return v.toFixed(1) + ' io/s';
		if (unit === 'cores') return v >= 0.1 || v === 0 ? v.toFixed(2) : v.toPrecision(2);
		return String(v);
	}

	// A stacked chart plots cumulative values (each series on top of the ones
	// before it) with fills between adjacent series; the legend/cursor still
	// reads the ORIGINAL per-series value via the value formatter below.
	function makeData(c: MetricChart): uPlot.AlignedData {
		if (!c.stacked) {
			return [c.times, ...c.series.map((s) => s.values)] as unknown as uPlot.AlignedData;
		}
		const accum = Array(c.times.length).fill(0);
		const stacked = c.series.map((s) =>
			s.values.map((v, i) => (accum[i] += v ?? 0))
		);
		return [c.times, ...stacked] as unknown as uPlot.AlignedData;
	}

	function makeOpts(c: MetricChart, w: number): uPlot.Options {
		return {
			width: w,
			height: 150,
			padding: [8, 10, 0, 0],
			legend: { show: true, live: true },
			cursor: { points: { size: 6 } },
			scales: { x: { time: true } },
			// Fill between adjacent stacked series (the bottom one fills to zero
			// via its own fill).
			bands: c.stacked
				? c.series.slice(1).map((_s, i) => ({ series: [i + 2, i + 1] as [number, number] }))
				: undefined,
			series: [
				{},
				...c.series.map((s, i) => ({
					label: s.name,
					stroke: palette[i % palette.length],
					fill: c.stacked ? palette[i % palette.length] + '4d' : undefined,
					width: 1.5,
					points: { show: false },
					// On stacked charts the plotted value is cumulative; report the
					// series' own value from the source chart instead.
					value: (_u: uPlot, v: number, si?: number, idx?: number | null) =>
						c.stacked && si != null && idx != null
							? fmt(c.unit, c.series[si - 1]?.values[idx] ?? null)
							: fmt(c.unit, v)
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
