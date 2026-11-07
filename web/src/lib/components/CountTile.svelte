<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Check } from '@lucide/svelte';

	// One number worth acting on, with what it counts and where to go about it.
	// Tinted only when tone says the number is a problem. Without a value the
	// label is the headline: an ok tone draws a check ("Nothing close to full").
	// children replaces the plain body text, for a body that needs markup.
	let {
		value,
		label,
		tone = 'none',
		body,
		link,
		children,
	}: {
		value?: number | string;
		label: string;
		tone?: 'none' | 'ok' | 'warn' | 'danger';
		body?: string;
		link?: { label: string; href?: string; onclick?: () => void };
		children?: Snippet;
	} = $props();

	const TILE = {
		none: 'border-line bg-panel',
		ok: 'border-line bg-panel',
		warn: 'border-warn/50 bg-warn-soft',
		danger: 'border-danger/30 bg-danger-soft',
	};
	const INK = {
		none: 'text-ink',
		ok: 'text-ink',
		warn: 'text-warn-ink',
		danger: 'text-danger-ink',
	};
	const shown = $derived(typeof value === 'number' ? value.toLocaleString('en-US') : value);
</script>

<div class="flex min-w-0 flex-col gap-1 rounded-lg border px-4 py-3.5 {TILE[tone]}">
	{#if shown !== undefined}
		<div class="text-[28px] leading-[34px] font-bold {INK[tone]}">{shown}</div>
		<div class="text-sm font-medium {INK[tone]}">{label}</div>
	{:else}
		<div class="flex min-h-[34px] items-center gap-2 text-lg leading-6 font-bold {INK[tone]}">
			{#if tone === 'ok'}<Check size={22} strokeWidth={2.2} class="shrink-0 text-ok" />{/if}
			<span>{label}</span>
		</div>
	{/if}
	{#if children}
		<div class="text-xs leading-4 text-ink-muted">{@render children()}</div>
	{:else if body}
		<p class="text-xs leading-4 text-ink-muted">{body}</p>
	{/if}
	{#if link}
		<div class="mt-auto pt-2 text-[13px] font-medium">
			{#if link.href}
				<a href={link.href} onclick={link.onclick} class="text-accent-ink hover:underline"
					>{link.label}</a
				>
			{:else}
				<button onclick={link.onclick} class="text-accent-ink hover:underline">{link.label}</button>
			{/if}
		</div>
	{/if}
</div>
