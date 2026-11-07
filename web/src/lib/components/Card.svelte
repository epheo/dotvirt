<script lang="ts">
	import type { Snippet } from 'svelte';

	// The one card: a panel with a header strip. meta is muted text after the
	// title ("3 of 12"); link or actions sit on the right. flush drops the body
	// padding for tables and divided lists, which inset their cells with px-card
	// so they line up with the title. class is for layout only (flex basis, grid
	// span).
	let {
		title,
		meta,
		link,
		actions,
		flush = false,
		class: cls = '',
		children,
	}: {
		title: string;
		meta?: string;
		link?: { label: string; href: string };
		actions?: Snippet;
		flush?: boolean;
		class?: string;
		children: Snippet;
	} = $props();

	const id = $props.id();
</script>

<section
	aria-labelledby={id}
	class="flex min-w-0 flex-col rounded-lg border border-line bg-panel [--card-x:18px] {cls}"
>
	<div
		class="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
	>
		<h2 {id} class="text-sm leading-5 font-bold text-ink">{title}</h2>
		{#if meta}<span class="text-[13px] text-ink-faint">{meta}</span>{/if}
		{#if actions || link}
			<span class="ml-auto flex flex-wrap items-center gap-3 text-[13px]">
				{#if actions}{@render actions()}{/if}
				{#if link}
					<a href={link.href} class="text-accent-ink hover:underline">{link.label}</a>
				{/if}
			</span>
		{/if}
	</div>
	{#if flush}
		{@render children()}
	{:else}
		<div class="px-card py-4 text-sm">{@render children()}</div>
	{/if}
</section>
