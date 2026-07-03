<script lang="ts">
	import { ChevronDown, ChevronRight } from 'lucide-svelte';
	import type { Snippet } from 'svelte';

	// One row of the inventory tree. Every row kind — pinned destination, group,
	// container, VM leaf — renders through this so indentation, hover and the
	// selection highlight cannot drift apart. The chevron and the label are
	// separate hit-areas: the chevron only collapses, the label activates.
	let {
		indent = 0,
		active = false,
		expanded = undefined,
		alignChevron = false,
		border = false,
		title = undefined,
		onactivate,
		ontoggle = undefined,
		oncontextmenu = undefined,
		icon = undefined,
		trailing = undefined,
		children
	}: {
		indent?: 0 | 1 | 2 | 3;
		active?: boolean;
		expanded?: boolean; // undefined = leaf (no chevron)
		alignChevron?: boolean; // leaf at a chevroned level: renders a spacer
		border?: boolean; // bottom hairline (pinned destinations)
		title?: string;
		onactivate: () => void;
		ontoggle?: () => void;
		oncontextmenu?: (e: MouseEvent) => void;
		icon?: Snippet;
		trailing?: Snippet;
		children: Snippet;
	} = $props();

	const INDENT = ['pl-2', 'pl-5', 'pl-7', 'pl-12'] as const;
</script>

<div
	class="flex w-full items-center gap-1 py-1 pr-2 hover:bg-select-soft {INDENT[indent]}
		{active ? 'bg-select hover:bg-select' : ''} {border ? 'border-b border-line' : ''}"
>
	{#if expanded !== undefined}
		<button class="flex w-3 items-center text-ink-faint" onclick={ontoggle} title="Expand/collapse">
			{#if expanded}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
		</button>
	{:else if alignChevron}
		<span class="w-3"></span>
	{/if}
	<button
		class="flex min-w-0 flex-1 items-center gap-1 text-left"
		onclick={onactivate}
		{oncontextmenu}
		{title}
	>
		{#if icon}{@render icon()}{/if}
		{@render children()}
		{#if trailing}
			<span class="ml-auto flex shrink-0 items-center gap-1">{@render trailing()}</span>
		{/if}
	</button>
</div>
