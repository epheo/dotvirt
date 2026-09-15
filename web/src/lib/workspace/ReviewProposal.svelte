<script lang="ts">
	import { ExternalLink, GitPullRequest } from 'lucide-svelte';
	import type { Proposal } from '$lib/api';
	import { approvalLine, checksPill } from '$lib/review';
	import { reviewCache } from '$lib/state/reviewCache.svelte';
	import Button from '$lib/components/Button.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import GitOpsStepper from '$lib/components/GitOpsStepper.svelte';
	import Skeleton from '$lib/components/Skeleton.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';
	import ReviewItems from './ReviewItems.svelte';
	import ReviewPane from './ReviewPane.svelte';

	// An open pull request under review: its diff and review state. The one
	// action on a proposal lives in the forge; the footer deep-links it.
	let { proposal: p }: { proposal: Proposal } = $props();

	const appr = $derived(approvalLine(p));
	const chk = $derived(checksPill(p));
	$effect(() => {
		reviewCache.loadProposal(p.project, p.prNumber);
	});
	const review = $derived(reviewCache.proposal(p.project, p.prNumber));
</script>

<ReviewPane title={p.title || `PR #${p.prNumber}`}>
	{#snippet icon()}<GitPullRequest size={16} class="text-accent-ink" />{/snippet}
	{#snippet head()}
		<span class="shrink-0 text-xs text-ink-faint">{p.project}</span>
		<Button
			variant="link"
			size="sm"
			class="ml-auto shrink-0"
			href={p.prURL}
			target="_blank"
			rel="noopener">PR #{p.prNumber} <ExternalLink size={12} /></Button
		>
	{/snippet}

	<div class="flex items-center gap-2 text-xs text-ink-muted">
		{#if p.by}<span>{p.mine ? 'yours' : `by ${p.by}`}</span>{/if}
		{#if p.revert}<span>· undoes a past change</span>{/if}
		{#if chk}<StatusPill tone={chk.tone} label={chk.text} />{/if}
		{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
	</div>
	{#if review?.error}
		<ErrorNote error={review.error} />
		<Button
			variant="secondary"
			size="sm"
			onclick={() => reviewCache.loadProposal(p.project, p.prNumber, true)}>Retry</Button
		>
	{:else if !review?.data}
		<Skeleton rows={2} class="h-16" />
	{:else}
		<ReviewItems items={review.data.items} empty="This pull request changes no manifests." />
	{/if}

	{#snippet footer()}
		<div class="mb-2">
			<GitOpsStepper stage="proposed" prNumber={p.prNumber} prUrl={p.prURL} />
		</div>
		<div class="flex items-center gap-3">
			<Button variant="secondary" class="shrink-0" href={p.prURL} target="_blank" rel="noopener">
				Open PR to approve and merge <ExternalLink size={13} />
			</Button>
			<span class="text-[11px] text-ink-faint">
				Approval and merge happen in the forge; once merged, ArgoCD applies the change and the
				result shows here and in Recent tasks within seconds.
			</span>
		</div>
	{/snippet}
</ReviewPane>
