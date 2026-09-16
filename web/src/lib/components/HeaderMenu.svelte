<script lang="ts">
	// A button-anchored dropdown for the top bar: a trigger snippet (rendered in the
	// dark header) over a light popover of menu items. Closes on Escape or a click
	// outside its own subtree. The trigger snippet gets {open, toggle}; the menu body
	// gets {close} so an item can dismiss the menu after acting.
	import type { Snippet } from 'svelte';
	import { dismiss } from '$lib/dismiss';

	let {
		align = 'left',
		class: className = '',
		panel = true,
		trigger,
		children,
	}: {
		align?: 'left' | 'right';
		class?: string;
		// false when the menu body draws its own panel chrome (e.g. ActionMenu);
		// the dropdown wrapper then only positions.
		panel?: boolean;
		trigger: Snippet<[{ open: boolean; toggle: () => void }]>;
		children: Snippet<[{ close: () => void }]>;
	} = $props();

	let open = $state(false);

	const toggle = () => (open = !open);
	const close = () => (open = false);
</script>

<div class="relative {className}" {@attach open && dismiss(close)}>
	{@render trigger({ open, toggle })}
	{#if open}
		<div
			class="absolute z-50 mt-1 {align === 'right' ? 'right-0' : 'left-0'} {panel
				? 'min-w-[12rem] rounded border border-line bg-panel py-1 text-xs text-ink-soft shadow-lg'
				: ''}"
		>
			{@render children({ close })}
		</div>
	{/if}
</div>
