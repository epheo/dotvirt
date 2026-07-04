<script lang="ts">
	import type { Snippet } from 'svelte';

	// One label/value line of an info card's <dl>. `children` (when given)
	// replaces the plain-text value, for values that need markup.
	let {
		label,
		value = '',
		mono = false,
		children,
	}: { label: string; value?: string; mono?: boolean; children?: Snippet } = $props();
</script>

<div class="flex justify-between gap-3 px-3 py-1.5">
	<dt class="shrink-0 text-slate-500">{label}</dt>
	{#if children}
		<dd class="min-w-0 text-right">{@render children()}</dd>
	{:else}
		<dd
			class="min-w-0 truncate text-right {mono
				? 'font-mono text-xs text-slate-600'
				: 'text-slate-800'}"
		>
			{value || '—'}
		</dd>
	{/if}
</div>
