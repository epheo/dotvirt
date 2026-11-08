<script lang="ts">
	import { ExternalLink, TriangleAlert, Undo2 } from '@lucide/svelte';
	import type { Commit, ProposeResult } from '#lib/api.js';
	import { shortDate } from '#lib/format.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import GitOpsStepper from '#lib/components/GitOpsStepper.svelte';
	import Note from '#lib/components/Note.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import ReviewItems from './ReviewItems.svelte';
	import ReviewPane from './ReviewPane.svelte';
	import ReviewSection from './ReviewSection.svelte';

	// A past change under review: the same field diff a staged item shows,
	// plus what reverting it now would do. Undo is the one action, itself a
	// pull request; its outcome stays with the commit so the card keeps
	// pointing at the PR.
	let {
		project,
		hash,
		commit,
		done,
		onundo,
	}: {
		project: string;
		hash: string;
		// null until the history row lands; the review itself names the commit.
		commit: Commit | null;
		// This session's revert of it.
		done?: ProposeResult;
		onundo: () => void;
	} = $props();

	$effect(() => {
		reviewCache.loadCommit(project, hash);
	});
	const review = $derived(reviewCache.commit(project, hash));
	const detail = $derived(review?.data ?? null);
	const may = $derived(inventory.canRevert(project));
</script>

<ReviewPane title={commit?.title ?? hash.slice(0, 8)}>
	{#snippet sub()}
		{#if commit}
			<code class="font-mono">{commit.shortHash}</code> in {project}, by {commit.author},
			{shortDate(commit.when)}{commit.merge ? ', merged' : ''}.
		{:else}
			In {project}.
		{/if}
	{/snippet}
	{#snippet head()}
		{#if commit?.prURL}
			<a
				href={commit.prURL}
				target="_blank"
				rel="noopener"
				class="inline-flex items-center gap-1 text-[13px] text-accent-ink hover:underline"
				>PR #{commit.prNumber} <ExternalLink size={12} /></a
			>
		{:else if commit?.prNumber}
			<span class="text-[13px] text-ink-muted">PR #{commit.prNumber}</span>
		{/if}
	{/snippet}

	<ReviewSection title="What this change did">
		{#if review?.error}
			<ErrorNote error={review.error} />
		{:else if !detail}
			<Skeleton rows={2} class="h-12" />
		{:else}
			<ReviewItems items={detail.items} empty="This commit changed no manifests." />
		{/if}
	</ReviewSection>

	{#snippet footer()}
		{#if done?.prURL}
			<GitOpsStepper stage="proposed" prNumber={done.prNumber} prUrl={done.prURL} />
			<p class="text-[13px] text-ink-soft">
				Undo proposed as
				<a
					href={done.prURL}
					target="_blank"
					rel="noopener"
					class="text-accent-ink underline underline-offset-2">PR #{done.prNumber}</a
				>. Approve and merge it in the forge; nothing changes until then.
			</p>
		{:else if done}
			<p class="text-[13px] text-ink-soft">
				Branch <code class="font-mono">{done.branch}</code> pushed.
				{#if done.compareURL}
					<a
						href={done.compareURL}
						target="_blank"
						rel="noopener"
						class="text-accent-ink underline underline-offset-2">Open the pull request</a
					>
				{:else}
					No forge is configured to open the pull request.
				{/if}
			</p>
		{:else}
			{#if detail?.revertWarning}
				<Note tone="warn" class="flex items-start gap-2">
					<TriangleAlert size={14} class="mt-0.5 shrink-0" />
					<span>{detail.revertWarning}</span>
				</Note>
			{/if}
			<div class="flex flex-wrap items-center gap-2">
				<Button
					variant="danger-secondary"
					size="sm"
					onclick={onundo}
					disabled={!detail || detail.reverted || !may}
					title={may ? undefined : 'Undo needs edit rights in every namespace of ' + project}
				>
					<Undo2 size={14} />Undo this change
				</Button>
				<StageChip kind="pr" />
			</div>
			{#if detail?.reverted}
				<p class="text-xs leading-4 text-ink-faint">
					Already undone: main matches the state before this change.
				</p>
			{:else if !may}
				<p class="text-xs leading-4 text-ink-faint">
					Undo needs edit rights in every namespace of {project}.
				</p>
			{:else}
				<Consequence size="sm" tone="warn">
					Opens a pull request that puts every file this change touched back to its previous state.
					It does not roll back to this point. Nothing changes until the pull request merges.
				</Consequence>
			{/if}
		{/if}
	{/snippet}
</ReviewPane>
