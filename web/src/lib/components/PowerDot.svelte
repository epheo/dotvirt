<script lang="ts">
	import type { Power } from '$lib/api';
	let { power, paused = false }: { power: Power; paused?: boolean } = $props();

	// A paused VMI stays phase Running, so call it out (amber) rather than green.
	const color = $derived(
		paused
			? 'bg-amber-400'
			: power === 'On'
				? 'bg-green-500'
				: power === 'Off'
					? 'bg-slate-400'
					: 'bg-amber-400'
	);
	const title = $derived(paused ? 'Paused' : `Power: ${power}`);
</script>

<span class="inline-block h-2.5 w-2.5 rounded-full {color}" {title}></span>
