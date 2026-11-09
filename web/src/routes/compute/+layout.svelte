<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { sectionRoot } from '#lib/nav.js';
	import { computeOff, firstSection } from '#lib/state/modules.svelte.js';

	let { children }: { children: Snippet } = $props();

	// Only the root leaves: a project's own page holds its permissions and
	// configuration whatever the cluster runs.
	const leaving = $derived(computeOff() && !page.params.project);
	// A value, so a re-read of the answer during the redirect redirects no second time.
	const next = $derived(leaving ? firstSection() : null);
	$effect(() => {
		if (next) goto(sectionRoot(next), { replace: true });
	});
</script>

{#if !leaving}
	{@render children()}
{/if}
