<script lang="ts">
	import { clusterGate } from '#lib/gates.js';
	import { hrefForScope } from '#lib/nav.js';

	// A host's name: a link for a caller who has the Hosts section, plain text
	// for one who does not, so no page sends them to a refusal.
	let {
		node,
		label = node,
		class: cls = '',
	}: { node: string; label?: string; class?: string } = $props();
</script>

{#if clusterGate.hosts().ok}<a href={hrefForScope({ kind: 'node', node })} class={cls}>{label}</a
	>{:else}{label}{/if}
