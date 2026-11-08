<script lang="ts">
	import { ExternalLink } from '@lucide/svelte';
	import type { Proposal } from '#lib/api.js';
	import { approvalLine, checksPill } from '#lib/review.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import GitOpsStepper from '#lib/components/GitOpsStepper.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';
	import ReviewItems from './ReviewItems.svelte';
	import ReviewPane from './ReviewPane.svelte';
	import ReviewSection from './ReviewSection.svelte';

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
	{#snippet sub()}
		<a
			href={p.prURL}
			target="_blank"
			rel="noopener"
			class="inline-flex items-center gap-1 text-accent-ink hover:underline"
			>PR #{p.prNumber} <ExternalLink size={12} /></a
		>
		in {p.project}{#if p.by},
			{p.mine ? 'yours' : `by ${p.by}`}{/if}{#if p.revert}, undoes a past change{/if}.
	{/snippet}
	{#snippet head()}
		{#if chk}<StatusPill tone={chk.tone} label={chk.text} />{/if}
		{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
	{/snippet}

	<ReviewSection title="Changes in this pull request">
		{#if review?.error}
			<ErrorNote error={review.error} />
			<div>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => reviewCache.loadProposal(p.project, p.prNumber, true)}>Retry</Button
				>
			</div>
		{:else if !review?.data}
			<Skeleton rows={2} class="h-12" />
		{:else}
			<ReviewItems items={review.data.items} empty="This pull request changes no manifests." />
		{/if}
	</ReviewSection>

	{#snippet footer()}
		<GitOpsStepper stage="proposed" prNumber={p.prNumber} prUrl={p.prURL} />
		<div class="mt-1 flex flex-wrap items-center gap-2">
			<Button href={p.prURL} target="_blank" rel="noopener">
				Open PR to approve and merge <ExternalLink size={14} />
			</Button>
		</div>
		<p class="text-xs leading-4 text-ink-faint">
			Approval and merge happen in the forge; once merged, ArgoCD applies the change and the result
			shows here and in Recent tasks within seconds.
		</p>
	{/snippet}
</ReviewPane>
