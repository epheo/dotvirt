<script lang="ts">
	// Staged "current -> future" readout: the committed value struck through,
	// the staged value in accent — the one rendering of a pending edit.
	let { from, to }: { from: string; to: string } = $props();
</script>

<span class="text-ink-faint line-through">{from}</span>
<span class="text-accent">{to}</span>
