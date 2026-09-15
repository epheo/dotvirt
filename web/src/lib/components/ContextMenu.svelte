<script lang="ts">
	// Generic right-click menu shell: a fixed-position container at the cursor,
	// clamped to the viewport, dismissed by click-away / another right-click /
	// Escape. Content comes from the children snippet (ActionMenu for VM rows, the
	// bulk or container panels otherwise).
	import type { Snippet } from 'svelte';
	import { dismiss } from '$lib/dismiss';

	let {
		x,
		y,
		onclose,
		children,
	}: {
		x: number;
		y: number;
		onclose: () => void;
		children: Snippet;
	} = $props();

	// Clamp so the panel never opens off-screen (estimate its footprint; exact
	// measurement isn't worth a double render).
	const px = $derived(Math.max(4, Math.min(x, window.innerWidth - 208)));
	const py = $derived(Math.max(4, Math.min(y, window.innerHeight - 340)));
</script>

<div class="fixed z-50" style="left: {px}px; top: {py}px" {@attach dismiss(onclose)}>
	{@render children()}
</div>
