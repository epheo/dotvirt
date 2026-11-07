<script lang="ts">
	import type { Snippet } from 'svelte';
	import StatusDot from '#lib/components/StatusDot.svelte';

	// The "N more" row of a folded rail group: a link to the container's VMs
	// tab, or a button that unfolds the group in place. tone carries the worst
	// problem among what is folded, so a fold never hides one.
	let {
		href,
		onclick,
		indent = 0,
		tone,
		count,
		children,
	}: {
		href?: string;
		onclick?: () => void;
		// The level whose labels the row lines up with: 0 under groups, 2 and 3
		// under VM leaves at those depths.
		indent?: 0 | 2 | 3;
		tone?: 'danger' | 'warn' | 'info';
		count?: string;
		children: Snippet;
	} = $props();

	const PAD = { 0: 'pl-7', 2: 'pl-[46px]', 3: 'pl-[66px]' } as const;
	const cls = $derived(
		`flex w-full items-center gap-1.5 rounded py-1.5 pr-2 text-left text-side-link hover:bg-side-hover/60 hover:underline ${PAD[indent]}`,
	);
</script>

{#snippet body()}
	<span class="truncate">{@render children()}</span>
	{#if tone}<StatusDot {tone} size="xs" title="A problem among them, listed under Issues" />{/if}
	{#if count}<span class="ml-auto text-xs text-side-dim">{count}</span>{/if}
{/snippet}

{#if href}
	<a {href} class={cls}>{@render body()}</a>
{:else}
	<button type="button" {onclick} class={cls}>{@render body()}</button>
{/if}
