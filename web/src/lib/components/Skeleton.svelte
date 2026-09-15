<script lang="ts">
	// Placeholder bars while a list loads. class sizes one bar; bg names the
	// ground (the dark sidebar has its own).
	let {
		rows = 1,
		class: cls = 'h-4',
		bg = 'bg-inset-strong',
	}: { rows?: number; class?: string; bg?: string } = $props();
</script>

<div class="space-y-2">
	{#each { length: rows } as _, i (i)}
		<div class="animate-pulse rounded {bg} {cls}"></div>
	{/each}
</div>
