<script lang="ts">
	import type { Power } from '$lib/api';
	let { power }: { power: Power } = $props();

	const color = $derived(
		power === 'On' ? 'bg-green-500' : power === 'Off' ? 'bg-slate-400' : 'bg-amber-400'
	);
	const title = $derived(`Power: ${power}`);
</script>

<span class="inline-block h-2.5 w-2.5 rounded-full {color}" {title}></span>
