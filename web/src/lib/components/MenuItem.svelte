<script lang="ts">
	import type { Snippet } from 'svelte';

	// One row of a dropdown or context menu.
	let {
		danger = false,
		disabled = false,
		title = undefined,
		onclick,
		icon = undefined,
		children,
	}: {
		danger?: boolean;
		disabled?: boolean;
		title?: string;
		onclick: () => void;
		icon?: Snippet;
		children: Snippet;
	} = $props();
</script>

<button
	{onclick}
	{disabled}
	{title}
	class="flex w-full items-center gap-2 px-3 py-1.5 text-left {danger
		? 'text-danger-ink hover:bg-danger-soft/60'
		: 'text-ink-soft hover:bg-inset'} disabled:cursor-not-allowed disabled:text-ink-faint disabled:hover:bg-transparent"
>
	{#if icon}{@render icon()}{/if}
	{@render children()}
</button>
