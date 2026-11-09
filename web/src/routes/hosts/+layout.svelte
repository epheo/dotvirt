<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { clusterGate } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';

	let { children }: { children: Snippet } = $props();

	// The pages mount on a yes only, so a caller without node rights never
	// reads them. Only a known no leaves: while the answer is out, or if it
	// failed, the URL stays and a reload keeps the page.
	const gate = $derived(clusterGate.hosts());
	$effect(() => {
		if (caps.cluster?.hosts !== false) return;
		untrack(() => ui.showToast(gate.why));
		goto('/compute', { replace: true });
	});
</script>

{#if gate.ok}
	{@render children()}
{:else}
	<p class="p-6 text-sm text-ink-muted">{gate.why}</p>
{/if}
