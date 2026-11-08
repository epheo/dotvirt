<script lang="ts">
	import { metricsGap, PARTIAL_NOTE } from '#lib/metrics.js';
	import type { MetricsReason } from '#lib/model.gen.js';
	import Note from './Note.svelte';

	// Why a metrics view shows no numbers, or that the ones it shows are short.
	let { reason, class: cls = '' }: { reason?: MetricsReason; class?: string } = $props();
	const gap = $derived(metricsGap(reason));
</script>

{#if gap}
	<Note tone="neutral" class="text-sm {cls}">{gap}</Note>
{:else if reason === 'partial'}
	<p class="text-xs text-warn-ink {cls}">{PARTIAL_NOTE}</p>
{/if}
