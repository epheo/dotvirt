<script lang="ts">
	import type { Snippet } from 'svelte';

	// Label + control + optional validation note, the standard modal form row.
	// The control stays the caller's markup so bindings and variants (mono,
	// placeholder, selects) need no prop plumbing; error renders only when set.
	let {
		label,
		required = false,
		error = '',
		children,
	}: { label: string; required?: boolean; error?: string; children: Snippet } = $props();
</script>

<label class="block">
	<span class="mb-1 block text-ink-soft">
		{label}{#if required}<span class="text-danger">*</span>{/if}
	</span>
	{@render children()}
	{#if error}
		<p class="mt-1 text-xs text-warn-ink">{error}</p>
	{/if}
</label>
