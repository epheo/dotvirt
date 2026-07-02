<script lang="ts">
	// A button-anchored dropdown for the top bar: a trigger snippet (rendered in the
	// dark header) over a light popover of menu items. Closes on Escape or a click
	// outside its own subtree. The trigger snippet gets {open, toggle}; the menu body
	// gets {close} so an item can dismiss the menu after acting.
	import type { Snippet } from 'svelte';

	let {
		align = 'left',
		class: className = '',
		trigger,
		children
	}: {
		align?: 'left' | 'right';
		class?: string;
		trigger: Snippet<[{ open: boolean; toggle: () => void }]>;
		children: Snippet<[{ close: () => void }]>;
	} = $props();

	let open = $state(false);
	let root = $state<HTMLElement | null>(null);

	const toggle = () => (open = !open);
	const close = () => (open = false);

	// Bubble-phase window handlers: the trigger's own onclick runs first (so toggling
	// shut stays shut), then this fires — a click landing outside `root` dismisses.
	function onWindowClick(e: MouseEvent) {
		if (open && root && !root.contains(e.target as Node)) close();
	}
	function onKeydown(e: KeyboardEvent) {
		if (open && e.key === 'Escape') close();
	}
</script>

<svelte:window onclick={onWindowClick} onkeydown={onKeydown} />

<div class="relative {className}" bind:this={root}>
	{@render trigger({ open, toggle })}
	{#if open}
		<div
			class="absolute z-50 mt-1 min-w-[12rem] rounded border border-slate-200 bg-white py-1 text-xs text-slate-700 shadow-lg {align ===
			'right'
				? 'right-0'
				: 'left-0'}"
		>
			{@render children({ close })}
		</div>
	{/if}
</div>
