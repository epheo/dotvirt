<script lang="ts">
	import { ChevronDown, ChevronRight } from '@lucide/svelte';
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { keepTab } from '#lib/nav.js';

	// One row of the inventory tree. Every row kind - pinned destination, group,
	// container, VM leaf - renders through this so indentation, hover and the
	// selection highlight cannot drift apart. The chevron and the label are
	// separate hit-areas: the chevron only collapses, the label is a real link
	// (deep-linkable, middle-click works).
	let {
		indent = 0,
		active = false,
		expanded = undefined,
		alignChevron = false,
		href,
		ontoggle = undefined,
		oncontextmenu = undefined,
		icon = undefined,
		trailing = undefined,
		children,
	}: {
		indent?: 0 | 1 | 2 | 3;
		active?: boolean;
		expanded?: boolean; // undefined = leaf (no chevron)
		alignChevron?: boolean; // leaf at a chevroned level: renders a spacer
		href: string;
		ontoggle?: () => void;
		oncontextmenu?: (e: MouseEvent) => void;
		icon?: Snippet;
		trailing?: Snippet;
		children: Snippet;
	} = $props();

	const INDENT = ['pl-2', 'pl-5', 'pl-7', 'pl-12'] as const;
	// Switching objects keeps the tab in view (see keepTab).
	const target = $derived(keepTab(href, page.url.searchParams.get('tab')));
</script>

<div
	class="flex w-full items-center gap-1.5 rounded py-1.5 pr-2 {INDENT[indent]} {active
		? 'bg-side-hover text-side-ink'
		: 'text-side-dim hover:bg-side-hover/60'}"
>
	{#if expanded !== undefined}
		<button
			class="flex w-3.5 shrink-0 items-center text-side-dim hover:text-side-ink"
			onclick={ontoggle}
			aria-expanded={expanded}
			title="Expand/collapse"
		>
			{#if expanded}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
		</button>
	{:else if alignChevron}
		<span class="w-3.5 shrink-0"></span>
	{/if}
	<a
		class="flex min-w-0 flex-1 items-center gap-1.5 text-left"
		href={target}
		aria-current={active ? 'page' : undefined}
		{oncontextmenu}
	>
		{#if icon}{@render icon()}{/if}
		{@render children()}
		{#if trailing}
			<span class="ml-auto flex shrink-0 items-center gap-1.5">{@render trailing()}</span>
		{/if}
	</a>
</div>
