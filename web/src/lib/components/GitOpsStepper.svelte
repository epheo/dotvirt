<script lang="ts">
	// The pipeline every config change rides: staged in a draft → proposed as a
	// pull request → merged to main → synced onto the cluster by ArgoCD. The one
	// drawing of the write model, reused anywhere a change's position is shown.
	let {
		stage,
		prNumber = undefined,
		prUrl = undefined,
		compact = false,
	}: {
		stage: 'staged' | 'proposed' | 'merged' | 'synced';
		prNumber?: number;
		prUrl?: string;
		compact?: boolean;
	} = $props();

	const STEPS = ['staged', 'proposed', 'merged', 'synced'] as const;
	const LABELS = { staged: 'Staged', proposed: 'Proposed', merged: 'Merged', synced: 'Synced' };
	const idx = $derived(STEPS.indexOf(stage));
</script>

{#if compact}
	<span
		class="inline-flex items-center gap-0.5"
		title="Staged → Proposed → Merged → Synced: {LABELS[stage]}"
	>
		{#each STEPS as s, i (s)}
			<span class="h-1.5 w-1.5 rounded-full {i <= idx ? 'bg-accent' : 'bg-line-strong'}"></span>
		{/each}
	</span>
{:else}
	<div class="flex items-center gap-1 text-[11px]">
		{#each STEPS as s, i (s)}
			{#if i > 0}<span class="h-px w-4 {i <= idx ? 'bg-accent' : 'bg-line-strong'}"></span>{/if}
			<span
				class="flex items-center gap-1 {i === idx
					? 'font-medium text-accent-ink'
					: i < idx
						? 'text-accent-ink'
						: 'text-ink-faint'}"
			>
				<span
					class="h-2 w-2 rounded-full {i <= idx ? 'bg-accent' : 'bg-line-strong'} {i === idx
						? 'ring-2 ring-select'
						: ''}"
				></span>
				{#if s === 'proposed' && prNumber && prUrl && i <= idx}
					<a href={prUrl} target="_blank" rel="noopener" class="hover:underline">PR #{prNumber}</a>
				{:else}
					{LABELS[s]}
				{/if}
			</span>
		{/each}
	</div>
{/if}
