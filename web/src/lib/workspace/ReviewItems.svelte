<script lang="ts">
	import { Pencil, Plus, Trash2 } from 'lucide-svelte';
	import { vmKey, type DraftItem } from '$lib/api';
	import { itemKey } from '$lib/review';
	import { draftKindTone, TONE_PILL } from '$lib/status';
	import ChangeList from '$lib/components/ChangeList.svelte';
	import ManifestDiff from '$lib/components/ManifestDiff.svelte';

	// One rendering of reviewed items, for a past commit and an open PR alike.
	let { items, empty }: { items: DraftItem[]; empty: string } = $props();
</script>

{#each items as it (itemKey(it))}
	<section class="rounded border border-line">
		<div class="flex items-center gap-2 border-b border-line bg-inset px-3 py-1.5">
			{#if it.kind === 'delete'}<Trash2 size={13} class="shrink-0 text-danger-ink" />
			{:else if it.kind === 'create'}<Plus size={13} class="shrink-0 text-ok-ink" />
			{:else}<Pencil size={13} class="shrink-0 text-accent-ink" />{/if}
			<span class="text-[13px] font-medium text-ink">{it.namespace ? vmKey(it) : it.name}</span>
			<span class="rounded px-1.5 py-0.5 text-xs {TONE_PILL[draftKindTone(it.kind)]}"
				>{it.kind}</span
			>
		</div>
		<div class="px-3 py-2">
			<ChangeList changes={it.changes} />
		</div>
		{#if it.yaml}
			<details class="border-t border-line">
				<summary
					class="cursor-pointer px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
					>{it.baseYAML ? 'Manifest diff' : 'Manifest'}</summary
				>
				<ManifestDiff before={it.baseYAML} after={it.yaml} />
			</details>
		{/if}
	</section>
{:else}
	<p class="text-xs text-ink-faint">{empty}</p>
{/each}
