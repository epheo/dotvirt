<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { desktopsOff, desktopsOn } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';

	let { children }: { children: Snippet } = $props();

	// The pages mount on a yes only. Only a known no leaves: while the answer
	// is out, or if it failed, the URL stays and a reload keeps the page.
	// Booleans, so the module read's minute poll moves neither effect.
	const on = $derived(desktopsOn());
	const off = $derived(desktopsOff());
	$effect(() => {
		if (!off) return;
		untrack(() => ui.showToast('This cluster serves no desktops.'));
		goto('/compute', { replace: true });
	});

	// One pull per watermark move while a Desktops page is open, from the
	// first frame on: before it the watermark reads 0, and its landing would
	// pull twice.
	const streamed = $derived(inventory.inventory !== null);
	$effect(() => {
		void inventory.desktopsVersion;
		if (on && streamed) untrack(() => desktops.pull());
	});
</script>

{#if on}
	{@render children()}
{:else}
	<p class="p-6 text-sm text-ink-muted">Checking whether this cluster serves desktops.</p>
{/if}
