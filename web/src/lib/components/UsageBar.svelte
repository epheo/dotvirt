<script lang="ts">
	import { fmtUsage } from '$lib/format';
	import Sparkline from './Sparkline.svelte';

	let {
		label,
		used,
		total = 0,
		unit,
		color = '#2563eb',
		spark = []
	}: {
		label: string;
		used: number;
		total?: number;
		unit: 'pct' | 'bytes';
		color?: string;
		spark?: number[];
	} = $props();

	// Fill fraction: used/total for bytes; used itself (already a %) for pct.
	const pct = $derived(
		unit === 'pct' ? Math.min(100, used) : total > 0 ? Math.min(100, (used / total) * 100) : 0
	);
	// vCenter-style escalation green→amber→red as utilization nears capacity.
	const barColor = $derived(pct > 90 ? '#dc2626' : pct > 75 ? '#f59e0b' : color);
</script>

<div>
	<div class="flex items-baseline justify-between text-xs">
		<span class="text-slate-500">{label}</span>
		<span class="text-slate-700">
			{fmtUsage(unit, used)}{#if unit === 'bytes' && total > 0}{' '}<span class="text-slate-400"
					>of {fmtUsage(unit, total)}</span
				>{/if}
		</span>
	</div>
	<div class="mt-1 flex items-center gap-2">
		<div class="h-2 flex-1 overflow-hidden rounded-full bg-slate-100">
			<div class="h-full rounded-full" style="width:{pct}%;background-color:{barColor}"></div>
		</div>
		{#if spark.length > 1}<Sparkline values={spark} color={barColor} />{/if}
	</div>
</div>
