<script lang="ts">
	let { summary }: { summary: { label: string; value: string }[] } = $props();
</script>

<p class="mb-3 text-xs text-ink-muted">Review the changes below, then stage them into Changes.</p>
{#if summary.length === 0}
	<div class="rounded border border-line bg-inset p-3 text-xs text-ink-muted">
		No changes yet — adjust a setting in an earlier step.
	</div>
{:else}
	<dl class="divide-y divide-line-soft rounded border border-line text-[13px]">
		{#each summary as c, i (i)}
			<div class="flex justify-between gap-3 px-3 py-1.5">
				<dt class="shrink-0 text-ink-muted">{c.label}</dt>
				<dd class="min-w-0 truncate text-right text-ink-soft">{c.value}</dd>
			</div>
		{/each}
	</dl>
{/if}
