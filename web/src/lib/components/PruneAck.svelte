<script lang="ts">
	import { TriangleAlert } from '@lucide/svelte';
	import type { ObjectRef } from '#lib/api.js';

	// The explicit yes a propose needs when merging lets the sync delete live
	// objects git does not declare. Every propose path renders this, holds its
	// button until ack is set, and sends what was shown here: the server
	// refuses a merge that would delete more. hidden counts the objects in
	// namespaces the caller cannot see, which the merge deletes all the same.
	let {
		prune,
		hidden = 0,
		ack = $bindable(false),
	}: { prune: ObjectRef[]; hidden?: number; ack?: boolean } = $props();
	const total = $derived(prune.length + hidden);
</script>

<div class="space-y-2 rounded border border-danger/40 bg-danger-soft px-3 py-2.5">
	<p class="flex items-start gap-2 text-danger-ink">
		<TriangleAlert size={14} class="mt-0.5 shrink-0" />
		<span
			><strong>The sync deletes {total} object{total === 1 ? '' : 's'} from the cluster.</strong>
			Git main does not declare {total === 1 ? 'it' : 'them'}.</span
		>
	</p>
	<ul class="space-y-0.5 pl-6 font-mono text-xs text-ink">
		{#each prune as r (r.kind + r.namespace + r.name)}
			<li>
				{r.namespace ? `${r.namespace}/` : ''}{r.name}
				<span class="text-ink-muted">{r.kind}</span>
			</li>
		{/each}
		{#if hidden}
			<li class="font-sans text-ink-muted">
				{prune.length ? `${hidden} more` : `${hidden} object${hidden === 1 ? '' : 's'}`} outside your
				view
			</li>
		{/if}
	</ul>
	<div class="flex flex-wrap items-center gap-x-4 gap-y-1 pl-6">
		<label class="flex items-center gap-2 font-medium text-ink">
			<input type="checkbox" bind:checked={ack} />
			Delete {total === 1 ? 'it' : `these ${total}`} when this merges
		</label>
		<span class="text-xs text-ink-soft"
			>Not meant to go? Adopt {total === 1 ? 'it' : 'them'} into git before proposing.</span
		>
	</div>
</div>
