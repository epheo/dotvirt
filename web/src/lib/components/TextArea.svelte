<script lang="ts">
	import type { HTMLTextareaAttributes } from 'svelte/elements';
	import { acceptsSuggestion } from '$lib/suggest';

	// TextInput's multi-line twin, with the same suggest contract: the default
	// shows as the placeholder and Tab takes it. rows follows the longer of the
	// value and the suggestion so a multi-line default is readable in place.
	let {
		value = $bindable(''),
		mono = false,
		suggest,
		placeholder,
		rows = 2,
		maxRows = 12,
		class: cls = '',
		onkeydown,
		...rest
	}: {
		value?: string;
		mono?: boolean;
		suggest?: string;
		rows?: number;
		maxRows?: number;
		class?: string;
	} & Omit<HTMLTextareaAttributes, 'value' | 'class' | 'rows'> = $props();

	const lines = $derived(
		Math.min(maxRows, Math.max(rows, (value || suggest || '').split('\n').length)),
	);
</script>

<span class="relative block w-full {cls}">
	<textarea
		bind:value
		rows={lines}
		placeholder={suggest || placeholder}
		onkeydown={(e) => {
			if (acceptsSuggestion(e, value, suggest)) value = suggest ?? '';
			else onkeydown?.(e);
		}}
		class="peer w-full rounded border border-line-strong px-2 py-1.5 text-sm focus:border-accent/60 disabled:bg-inset-strong disabled:text-ink-faint {mono
			? 'font-mono text-xs'
			: ''}"
		{...rest}></textarea>
	{#if suggest}
		<kbd
			class="pointer-events-none absolute top-1.5 right-1.5 rounded border border-line-strong bg-inset px-1 font-sans text-[10px] leading-4 text-ink-muted opacity-0 peer-focus:peer-placeholder-shown:opacity-100"
			>Tab</kbd
		>
	{/if}
</span>
