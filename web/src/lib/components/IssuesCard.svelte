<script lang="ts">
	import { CircleCheck } from '@lucide/svelte';
	import { issuesInScope } from '#lib/issues.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import IssueRow from './IssueRow.svelte';

	// The Summary lane of the issues plane: standing problems in this scope,
	// derived from the live stream - no fetch of its own. Rendered even when
	// clean so "no issues" is a stated fact, not an absence; the count outside
	// the scope keeps it honest against the header's total.
	let { scope = {} }: { scope?: { project?: string; namespace?: string } } = $props();

	const issues = $derived(issuesInScope(inventory.issues, scope));
	const elsewhere = $derived(inventory.issues.length - issues.length);
	const MAX = 6;
</script>

<section class="rounded border border-line">
	<h3
		class="flex items-center justify-between border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
	>
		<span>Issues</span>
		{#if issues.length}<span class="font-normal text-ink-faint normal-case"
				>{issues.length} in this scope, worst first</span
			>{/if}
	</h3>
	{#if !issues.length}
		<p class="flex items-center gap-2 px-3 py-3 text-xs text-ink-faint">
			<CircleCheck size={14} class="text-ok" /> No standing issues in this scope.
		</p>
	{:else}
		<ul class="divide-y divide-line-soft text-xs">
			{#each issues.slice(0, MAX) as i (i.scope + i.label)}
				<IssueRow issue={i} />
			{/each}
		</ul>
		{#if issues.length > MAX}
			<p class="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-ink-faint">
				and {issues.length - MAX} more
			</p>
		{/if}
	{/if}
	{#if elsewhere > 0}
		<p class="border-t border-line-soft px-3 py-1.5 text-[11px] text-ink-faint">
			{elsewhere} more outside this scope, listed under Issues in the header.
		</p>
	{/if}
</section>
