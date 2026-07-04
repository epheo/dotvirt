<script lang="ts">
	import type { Snippet } from 'svelte';
	import { TONE_DOT, TONE_PILL, type Tone } from '$lib/status';

	// The one status pill: tinted background + status ink, with a leading dot
	// (or a caller icon in its place). Renders a button when clickable so hosts
	// can hang detail popups off it.
	let {
		tone,
		label,
		dot = true,
		icon,
		title,
		onclick,
	}: {
		tone: Tone;
		label: string;
		dot?: boolean;
		icon?: Snippet;
		title?: string;
		onclick?: (e: MouseEvent) => void;
	} = $props();

	const cls = $derived(
		`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs ${TONE_PILL[tone]}`,
	);
</script>

{#if onclick}
	<button type="button" {onclick} {title} class="{cls} cursor-pointer hover:opacity-80">
		{#if icon}{@render icon()}{:else if dot}<span
				class="inline-block h-1.5 w-1.5 rounded-full {TONE_DOT[tone]}"
			></span>{/if}
		{label}
	</button>
{:else}
	<span class={cls} {title}>
		{#if icon}{@render icon()}{:else if dot}<span
				class="inline-block h-1.5 w-1.5 rounded-full {TONE_DOT[tone]}"
			></span>{/if}
		{label}
	</span>
{/if}
