<script lang="ts">
	import { hunks, lineDiff } from '$lib/textdiff';

	// The Manifest fold's body: the object's YAML as changed, or, when the
	// committed side is known, the line diff between the two. A VM edit
	// already reads as a field diff above; this is what a network, policy or
	// template edit has instead, and what a VM edit shows when no summarized
	// field moved (cloud-init, a device detail).
	let { before = '', after }: { before?: string; after: string } = $props();
	const rows = $derived(before ? hunks(lineDiff(before, after)) : null);
</script>

{#if rows}
	<pre
		class="overflow-x-auto px-3 pb-3 font-mono text-[11px] leading-snug text-ink-soft">{#each rows as r, i (i)}{#if r.kind === 'skip'}<span
					class="block text-ink-faint italic">    ... {r.count} unchanged line{r.count > 1
						? 's'
						: ''}</span
				>{:else if r.kind === 'add'}<span class="block bg-ok-soft text-ok-ink">+ {r.text}</span
				>{:else if r.kind === 'del'}<span class="block bg-danger-soft text-danger-ink"
					>- {r.text}</span
				>{:else}<span class="block">  {r.text}</span>{/if}{/each}</pre>
{:else}
	<pre
		class="overflow-x-auto px-3 pb-3 font-mono text-[11px] leading-snug text-ink-soft">{after}</pre>
{/if}
