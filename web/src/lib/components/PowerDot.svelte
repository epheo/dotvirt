<script lang="ts">
	import type { Power } from '#lib/api.js';
	import { powerTone } from '#lib/status.js';
	import StatusDot from './StatusDot.svelte';

	let { power, paused = false }: { power: Power; paused?: boolean } = $props();
</script>

<StatusDot
	tone={powerTone(power, paused)}
	size="md"
	title={paused ? 'Paused' : `Power: ${power}`}
/>
