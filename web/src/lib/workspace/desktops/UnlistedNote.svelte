<script lang="ts">
	// What the caller may not list, and who may let it, said in place of a list
	// that would read as empty. About its rights only: never whether such
	// objects exist.
	let { lines }: { lines: string[] } = $props();
</script>

{#if lines.length}
	<div class="rounded-lg border border-line bg-inset px-4 py-3 text-[13px] text-ink-soft">
		{#each lines as line (line)}<p>{line}</p>{/each}
	</div>
{/if}
