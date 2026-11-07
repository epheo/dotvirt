<script lang="ts">
	import { CircleCheck } from '@lucide/svelte';
	import { issuesInScope } from '#lib/issues.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import IssueRow from './IssueRow.svelte';

	// The Summary lane of the issues plane: standing problems in this scope,
	// derived from the live stream - no fetch of its own. Rendered even when
	// clean so "no issues" is a stated fact, not an absence; the count outside
	// the scope keeps it honest against the header's total.
	let {
		scope = {},
		label = 'this scope',
	}: { scope?: { project?: string; namespace?: string }; label?: string } = $props();

	const issues = $derived(issuesInScope(inventory.issues, scope));
	const elsewhere = $derived(inventory.issues.filter((i) => !issues.includes(i)));
	const MAX = 6;
</script>

<section class="rounded border border-line">
	<h3
		class="flex items-center justify-between border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
	>
		<span>Issues in {label}</span>
		{#if issues.length}<span class="font-normal text-ink-faint normal-case"
				>{issues.length} of {inventory.issues.length}. Ordered by severity.</span
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
	{#if elsewhere.length > 0}
		<!-- Named, so the header's total never reads as a mystery here. -->
		<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-muted">
			{elsewhere.length} more outside this scope:
			{#each elsewhere.slice(0, 2) as i, n (i.scope + i.label)}{n ? ', ' : ' '}<a
					href={i.href}
					class="text-accent-ink hover:underline">{i.scope}</a
				>{/each}{elsewhere.length > 2 ? `, and ${elsewhere.length - 2} more` : ''}.
		</p>
	{/if}
</section>
