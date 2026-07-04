<script lang="ts">
	import { TONE_DOT, type Tone } from '$lib/status';

	// The one status dot. `pulse` marks in-flight states (migrations, syncs).
	let {
		tone,
		size = 'sm',
		pulse = false,
		title,
	}: { tone: Tone; size?: 'xs' | 'sm' | 'md'; pulse?: boolean; title?: string } = $props();

	const dim = $derived({ xs: 'h-1.5 w-1.5', sm: 'h-2 w-2', md: 'h-2.5 w-2.5' }[size]);
</script>

<span
	class="inline-block shrink-0 rounded-full {dim} {TONE_DOT[tone]} {pulse ? 'animate-pulse' : ''}"
	{title}
></span>
