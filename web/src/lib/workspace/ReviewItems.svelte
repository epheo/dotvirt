<script lang="ts">
	import { Pencil, Plus, Trash2 } from '@lucide/svelte';
	import { vmKey, type DraftItem } from '#lib/api.js';
	import { itemKey } from '#lib/review.js';
	import { draftKindTone } from '#lib/status.js';
	import ChangeList from '#lib/components/ChangeList.svelte';
	import ManifestDiff from '#lib/components/ManifestDiff.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// One rendering of reviewed items, for a past commit and an open PR alike.
	let { items, empty }: { items: DraftItem[]; empty: string } = $props();
</script>

<ul class="flex flex-col divide-y divide-line-soft">
	{#each items as it (itemKey(it))}
		<li class="flex flex-col gap-2 py-2.5 first:pt-0 last:pb-0">
			<div class="flex items-center gap-2">
				{#if it.kind === 'delete'}<Trash2 size={14} class="shrink-0 text-danger-ink" />
				{:else if it.kind === 'create'}<Plus size={14} class="shrink-0 text-ok-ink" />
				{:else}<Pencil size={14} class="shrink-0 text-accent-ink" />{/if}
				<span class="min-w-0 truncate font-mono text-[13px] font-medium text-ink"
					>{it.namespace ? vmKey(it) : it.name}</span
				>
				<StatusPill tone={draftKindTone(it.kind)} label={it.kind} dot={false} />
			</div>
			<div class="pl-[22px]"><ChangeList changes={it.changes} /></div>
			{#if it.yaml}
				<details class="ml-[22px] overflow-hidden rounded-md border border-line-soft">
					<summary class="cursor-pointer px-3 py-1.5 text-[13px] text-accent-ink hover:underline"
						>{it.baseYAML ? 'Manifest diff' : 'Manifest'}</summary
					>
					<ManifestDiff before={it.baseYAML} after={it.yaml} />
				</details>
			{/if}
		</li>
	{:else}
		<li class="text-[13px] text-ink-faint">{empty}</li>
	{/each}
</ul>
