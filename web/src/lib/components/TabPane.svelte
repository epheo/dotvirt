<script lang="ts">
	import type { Snippet } from 'svelte';

	// The scroll region a tab body sits in. It takes focus on purpose (axe
	// scrollable-region-focusable): a region that scrolls must be reachable
	// by keyboard.
	let {
		label = 'Tab content',
		class: cls = '',
		children,
	}: { label?: string; class?: string; children: Snippet } = $props();
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="min-h-0 flex-1 overflow-y-auto {cls}" role="region" aria-label={label} tabindex="0">
	{@render children()}
</div>
