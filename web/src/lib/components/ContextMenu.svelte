<script lang="ts">
	// Generic right-click menu shell: a fixed-position container at the cursor,
	// clamped to the viewport, dismissed by click-away / another right-click /
	// Escape. Content comes from the children snippet (ActionMenu for VM rows, the
	// bulk or container panels otherwise).
	import type { Snippet } from 'svelte';

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

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') onclose();
	}
</script>

<svelte:window {onkeydown} />

<button
	class="fixed inset-0 z-40 cursor-default"
	onclick={onclose}
	oncontextmenu={(e) => {
		e.preventDefault();
		onclose();
	}}
	aria-label="Close menu"
	tabindex="-1"
></button>
<div class="fixed z-50" style="left: {px}px; top: {py}px">
	{@render children()}
</div>
