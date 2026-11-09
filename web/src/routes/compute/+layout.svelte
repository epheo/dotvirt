<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { sectionRoot } from '#lib/nav.js';
	import { computeOff, firstSection } from '#lib/state/modules.svelte.js';

	let { children }: { children: Snippet } = $props();

	// A value, so a re-read of the answer during the redirect redirects no second time.
	const next = $derived(computeOff() ? firstSection() : null);
	$effect(() => {
		if (next) goto(sectionRoot(next), { replace: true });
	});
</script>

{#if !computeOff()}
	{@render children()}
{/if}
