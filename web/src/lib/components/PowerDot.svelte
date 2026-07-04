<script lang="ts">
	import type { Power } from '$lib/api';
	import { powerTone } from '$lib/status';
	import StatusDot from './StatusDot.svelte';

	let { power, paused = false }: { power: Power; paused?: boolean } = $props();
</script>

<StatusDot
	tone={powerTone(power, paused)}
	size="md"
	title={paused ? 'Paused' : `Power: ${power}`}
/>
