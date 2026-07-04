<script lang="ts">
	// A tiny dependency-free inline trend line for the capacity/usage widgets.
	let {
		values,
		color = 'var(--chart-1)',
		width = 64,
		height = 18,
	}: { values: number[]; color?: string; width?: number; height?: number } = $props();

	const path = $derived.by(() => {
		if (!values || values.length < 2) return '';
		const min = Math.min(...values);
		const max = Math.max(...values);
		const span = max - min || 1;
		const n = values.length;
		return values
			.map((v, i) => {
				const x = (i / (n - 1)) * width;
				const y = height - 1 - ((v - min) / span) * (height - 2);
				return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
			})
			.join(' ');
	});
</script>

{#if path}
	<svg {width} {height} aria-hidden="true" class="shrink-0">
		<path d={path} fill="none" style:stroke={color} stroke-width="1" stroke-linejoin="round" />
	</svg>
{/if}
