<script lang="ts">
	import { X } from 'lucide-svelte';
	import type { Snippet } from 'svelte';

	// The right-side panel shell (Changes, and anything summoned from the header):
	// fixed width, hairline + shadow, titled header with a close affordance.
	let {
		title,
		count = undefined,
		onclose,
		footer = undefined,
		children
	}: {
		title: string;
		count?: number;
		onclose: () => void;
		footer?: Snippet;
		children: Snippet;
	} = $props();
</script>

<aside class="flex h-full w-[28rem] flex-col border-l border-line-strong bg-panel shadow-xl">
	<header class="flex items-center justify-between border-b border-line px-4 py-3">
		<h2 class="text-base font-semibold text-ink">
			{title}
			{#if count !== undefined}<span class="text-ink-faint">({count})</span>{/if}
		</h2>
		<button onclick={onclose} aria-label="Close" class="text-ink-faint hover:text-ink-soft">
			<X size={18} />
		</button>
	</header>
	{@render children()}
	{#if footer}
		<footer class="border-t border-line px-4 py-2 text-xs text-ink-faint">
			{@render footer()}
		</footer>
	{/if}
</aside>
