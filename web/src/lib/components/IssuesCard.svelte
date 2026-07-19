<script lang="ts">
	import { goto } from '$app/navigation';
	import { CircleCheck } from 'lucide-svelte';
	import { deriveIssues, issuesInScope } from '$lib/issues';
	import { inventory } from '$lib/state/inventory.svelte';
	import StatusDot from './StatusDot.svelte';

	// The Summary lane of the issues plane: standing problems in this scope,
	// derived from the live stream — no fetch of its own. Rendered even when
	// clean so "no issues" is a stated fact, not an absence.
	let { scope = {} }: { scope?: { project?: string; namespace?: string } } = $props();

	const issues = $derived(issuesInScope(deriveIssues(inventory.inventory), scope));
	const MAX = 6;
</script>

<section class="rounded border border-line">
	<h3
		class="flex items-center justify-between border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
	>
		<span>Issues</span>
		{#if issues.length}<span class="font-normal text-ink-faint normal-case"
				>{issues.length} need attention</span
			>{/if}
	</h3>
	{#if !issues.length}
		<p class="flex items-center gap-2 px-3 py-3 text-xs text-ink-faint">
			<CircleCheck size={14} class="text-ok" /> No standing issues in this scope.
		</p>
	{:else}
		<ul class="divide-y divide-line-soft text-[13px]">
			{#each issues.slice(0, MAX) as i (i.scope + i.label)}
				<li>
					<button
						onclick={() => goto(i.href)}
						class="flex w-full items-baseline gap-2 px-3 py-1.5 text-left hover:bg-inset"
						title={i.detail ?? ''}
					>
						<span class="self-center"><StatusDot tone={i.severity} size="xs" /></span>
						<span class="shrink-0 font-medium text-ink">{i.scope}</span>
						<span class="truncate text-ink-soft">{i.label}</span>
						{#if i.detail}<span class="min-w-0 flex-1 truncate text-right text-xs text-ink-faint"
								>{i.detail}</span
							>{/if}
					</button>
				</li>
			{/each}
		</ul>
		{#if issues.length > MAX}
			<p class="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-ink-faint">
				and {issues.length - MAX} more
			</p>
		{/if}
	{/if}
</section>
