<script lang="ts">
	// What the caller may not list, said in place of a list that would read
	// as empty. About its rights only: never whether such objects exist.
	let { lines }: { lines: string[] } = $props();
</script>

{#if lines.length}
	<div class="rounded-lg border border-line bg-inset px-4 py-3 text-[13px]">
		{#each lines as line (line)}<p class="text-ink-soft">{line}</p>{/each}
		<p class="text-ink-faint">A project admin can let you read them.</p>
	</div>
{/if}
