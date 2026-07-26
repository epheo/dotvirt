<script lang="ts" generics="T extends { removed: boolean }">
	import type { Snippet } from 'svelte';

	// Shared scaffolding for the device edit steps: live-count header with an
	// add button, numbered rows struck through when removed, a remove/undo
	// toggle, and an empty note. The row body is the caller's snippet; the
	// caller owns the array, so the toggle mutates through `ontoggle`.
	let {
		items,
		unit,
		addLabel,
		rowLabel,
		empty,
		onadd,
		ontoggle,
		row,
	}: {
		items: T[];
		unit: string;
		addLabel: string;
		rowLabel: string;
		empty: string;
		onadd: () => void;
		ontoggle: (item: T) => void;
		row: Snippet<[T]>;
	} = $props();

	const active = $derived(items.filter((r) => !r.removed).length);
</script>

<div class="mb-2 flex items-center justify-between">
	<span class="text-xs text-ink-faint">{active} {unit}</span>
	<button
		onclick={onadd}
		class="rounded border border-line-strong px-2 py-0.5 text-xs hover:bg-inset"
	>
		{addLabel}
	</button>
</div>
{#each items as item, i (i)}
	<div class="mb-1 flex items-center gap-2 {item.removed ? 'opacity-40 line-through' : ''}">
		<span class="w-32 truncate text-ink-soft">{rowLabel} {i + 1}</span>
		{@render row(item)}
		<button
			onclick={() => ontoggle(item)}
			class="ml-auto text-xs {item.removed ? 'text-accent' : 'text-danger'}"
		>
			{item.removed ? 'undo' : 'remove'}
		</button>
	</div>
{/each}
{#if active === 0}<p class="text-xs text-ink-faint">{empty}</p>{/if}
