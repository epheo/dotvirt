<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { desktopsOff, desktopsOn } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';

	let { children }: { children: Snippet } = $props();

	// The pages mount on a yes only. Only a known no leaves: while the answer
	// is out, or if it failed, the URL stays and a reload keeps the page. The
	// shell pulls the desktops while the section's tree shows.
	const on = $derived(desktopsOn());
	// A bool, so the module read's minute poll toasts no second time.
	const off = $derived(desktopsOff());
	$effect(() => {
		if (!off) return;
		untrack(() => ui.showToast('This cluster serves no desktops.'));
		goto('/compute', { replace: true });
	});
</script>

{#if on}
	{@render children()}
{:else}
	<p class="p-6 text-sm text-ink-muted">Checking whether this cluster serves desktops.</p>
{/if}
