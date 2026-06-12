<script lang="ts">
	import { fmtUsage } from '$lib/format';
	import Sparkline from './Sparkline.svelte';

	let {
		label,
		used,
		total,
		allocated = 0,
		unit,
		color = '#2563eb',
		spark = []
	}: {
		label: string;
		used: number;
		total: number;
		allocated?: number;
		unit: 'bytes' | 'cores';
		color?: string;
		spark?: number[];
	} = $props();

	const R = 38;
	const SW = 9;
	const C = 2 * Math.PI * R;
	const usedFrac = $derived(total > 0 ? Math.min(1, used / total) : 0);
	const allocFrac = $derived(total > 0 ? Math.min(1, allocated / total) : 0);
	const ringColor = $derived(usedFrac > 0.9 ? '#dc2626' : usedFrac > 0.75 ? '#f59e0b' : color);
</script>

<div class="flex flex-col items-center">
	<svg width="96" height="96" viewBox="0 0 96 96">
		<!-- capacity track -->
		<circle cx="48" cy="48" r={R} fill="none" stroke="#e2e8f0" stroke-width={SW} />
		<!-- allocated/committed segment (faint), under the used arc -->
		{#if allocated > 0}
			<circle
				cx="48"
				cy="48"
				r={R}
				fill="none"
				stroke="#cbd5e1"
				stroke-width={SW}
				stroke-dasharray="{allocFrac * C} {C}"
				transform="rotate(-90 48 48)"
			/>
		{/if}
		<!-- used arc -->
		<circle
			cx="48"
			cy="48"
			r={R}
			fill="none"
			stroke={ringColor}
			stroke-width={SW}
			stroke-linecap="round"
			stroke-dasharray="{usedFrac * C} {C}"
			transform="rotate(-90 48 48)"
		/>
		<text x="48" y="46" text-anchor="middle" class="fill-slate-800 text-[13px] font-semibold"
			>{fmtUsage(unit, used)}</text
		>
		<text x="48" y="60" text-anchor="middle" class="fill-slate-400 text-[9px]"
			>of {fmtUsage(unit, total)}</text
		>
	</svg>
	<div class="mt-0.5 text-xs font-medium text-slate-600">{label}</div>
	{#if allocated > 0}
		<div class="text-[10px] text-slate-400">{fmtUsage(unit, allocated)} allocated</div>
	{/if}
	{#if spark.length > 1}<div class="mt-0.5"><Sparkline values={spark} color={ringColor} width={84} height={16} /></div>{/if}
</div>
