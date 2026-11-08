<script lang="ts">
	import { TriangleAlert } from '@lucide/svelte';
	import type { ObjectRef } from '#lib/api.js';

	// The explicit yes a propose needs when merging lets the sync delete live
	// objects git no longer declares. Every propose path renders this and holds
	// its button until ack is set.
	let { prune, ack = $bindable(false) }: { prune: ObjectRef[]; ack?: boolean } = $props();
</script>

<div class="space-y-2 rounded border border-danger/40 bg-danger-soft px-3 py-2.5">
	<p class="flex items-start gap-2 text-danger-ink">
		<TriangleAlert size={14} class="mt-0.5 shrink-0" />
		<span
			><strong
				>The sync deletes {prune.length} object{prune.length === 1 ? '' : 's'} from the cluster.</strong
			>
			Git main no longer declares {prune.length === 1 ? 'it' : 'them'}.</span
		>
	</p>
	<ul class="space-y-0.5 pl-6 font-mono text-xs text-ink">
		{#each prune as r (r.kind + r.namespace + r.name)}
			<li>
				{r.namespace ? `${r.namespace}/` : ''}{r.name}
				<span class="text-ink-muted">{r.kind}</span>
			</li>
		{/each}
	</ul>
	<div class="flex flex-wrap items-center gap-x-4 gap-y-1 pl-6">
		<label class="flex items-center gap-2 font-medium text-ink">
			<input type="checkbox" bind:checked={ack} />
			Delete {prune.length === 1 ? 'it' : `these ${prune.length}`} when this merges
		</label>
		<span class="text-xs text-ink-soft"
			>Not meant to go? Adopt {prune.length === 1 ? 'it' : 'them'} into git first, from Issues.</span
		>
	</div>
</div>
