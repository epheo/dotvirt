<script lang="ts">
	import { ExternalLink, History, TriangleAlert } from 'lucide-svelte';
	import type { Commit, ProposeResult } from '$lib/api';
	import { shortDate } from '$lib/format';
	import { reviewCache } from '$lib/state/reviewCache.svelte';
	import Button from '$lib/components/Button.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import GitOpsStepper from '$lib/components/GitOpsStepper.svelte';
	import Note from '$lib/components/Note.svelte';
	import Skeleton from '$lib/components/Skeleton.svelte';
	import ReviewItems from './ReviewItems.svelte';
	import ReviewPane from './ReviewPane.svelte';

	// A past change under review: the same field diff a staged item shows,
	// plus what reverting it now would do. Undo is the one action, itself a
	// pull request; its outcome stays with the commit so the pane keeps
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
</script>

<ReviewPane title={commit?.title ?? hash.slice(0, 8)}>
	{#snippet icon()}<History size={16} class="text-ink-muted" />{/snippet}
	{#snippet head()}
		<span class="shrink-0 text-xs text-ink-faint">{project}</span>
		{#if commit?.prURL}
			<Button
				variant="link"
				size="sm"
				class="ml-auto shrink-0"
				href={commit.prURL}
				target="_blank"
				rel="noopener">PR #{commit.prNumber} <ExternalLink size={12} /></Button
			>
		{:else if commit?.prNumber}
			<span class="ml-auto shrink-0 text-xs text-ink-faint">PR #{commit.prNumber}</span>
		{/if}
	{/snippet}

	{#if commit}
		<p class="text-xs text-ink-muted">
			<code>{commit.shortHash}</code> · {commit.author} · {shortDate(commit.when)}
			{#if commit.merge}· merged{/if}
		</p>
	{/if}
	{#if review?.error}
		<ErrorNote error={review.error} />
	{:else if !detail}
		<Skeleton rows={2} class="h-16" />
	{:else}
		<ReviewItems items={detail.items} empty="This commit changed no manifests." />
	{/if}

	{#snippet footer()}
		{#if done?.prURL}
			<div class="mb-2 flex items-center gap-3">
				<GitOpsStepper stage="proposed" prNumber={done.prNumber} prUrl={done.prURL} />
			</div>
			<Note tone="neutral">
				Undo proposed as
				<a href={done.prURL} target="_blank" rel="noopener" class="underline">PR #{done.prNumber}</a
				>. Approve and merge it in the forge; nothing changes until then.
			</Note>
		{:else if done}
			<Note tone="neutral">
				Branch <code>{done.branch}</code> pushed —
				{#if done.compareURL}
					<a href={done.compareURL} target="_blank" rel="noopener" class="underline"
						>open the pull request</a
					>
				{:else}
					no forge configured.
				{/if}
			</Note>
		{:else}
			{#if detail?.revertWarning}
				<Note tone="warn" class="mb-2 flex items-start gap-2">
					<TriangleAlert size={14} class="mt-0.5 shrink-0" />
					<span>{detail.revertWarning}</span>
				</Note>
			{/if}
			<div class="flex items-center gap-3">
				<span class="text-[11px] text-ink-faint">
					{#if detail?.reverted}
						Already undone: main matches the state before this change.
					{:else}
						Undo opens a pull request that puts every file this change touched back to its previous
						state. It does not roll back to this point. Nothing changes until the pull request
						merges.
					{/if}
				</span>
				<Button
					variant="danger"
					class="ml-auto shrink-0"
					onclick={onundo}
					disabled={!detail || detail.reverted}
				>
					Undo this change
				</Button>
			</div>
		{/if}
	{/snippet}
</ReviewPane>
