<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import {
		ChevronDown,
		CircleCheck,
		GitBranch,
		GitPullRequest,
		LoaderCircle,
		TriangleAlert,
	} from '@lucide/svelte';
	import type { Change, DraftItem } from '#lib/api.js';
	import { changesHref, hrefForScope } from '#lib/nav.js';
	import { approvalLine, checksPill, reviewURL } from '#lib/review.js';
	import { TONE_PILL, TONE_TEXT } from '#lib/status.js';
	import { drafts, PLATFORM_PROJECT } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { trayLanes, type TrayLane } from '#lib/tray.js';
	import Button from './Button.svelte';

	// The one place a change of this user's is followed from staged to live:
	// it replaces the header Propose button, the Changes dot and the pending
	// banners. Every lane names what happens next and who acts. Hidden on
	// /changes, which is the full view of the same pipeline.
	const lanes = $derived(
		trayLanes({
			drafts: drafts.drafts,
			proposals: inventory.proposals,
			followed: tray.followed,
			sync: (p) =>
				p === PLATFORM_PROJECT
					? inventory.inventory?.platform
					: inventory.projects.find((x) => x.name === p)?.gitOps,
		}),
	);

	// Follow on the stable keys, never the per-frame arrays.
	$effect(() => {
		inventory.proposalsKey;
		inventory.taskFeed;
		untrack(() => tray.update(inventory.proposals, inventory.taskFeed));
	});

	// A live merge has said what it had to; it leaves by itself.
	const liveKeys = $derived(
		lanes
			.filter((l) => l.kind === 'live')
			.map((l) => l.key)
			.join(' '),
	);
	$effect(() => {
		const keys = liveKeys ? liveKeys.split(' ') : [];
		if (!keys.length) return;
		const t = setTimeout(() => keys.forEach((k) => tray.dismiss(k)), 15_000);
		return () => clearTimeout(t);
	});

	const hidden = $derived(page.url.pathname.startsWith('/changes') || lanes.length === 0);

	// Unfolded while something needs this user (a draft to propose, a refusal,
	// a merge that just went live) or once they open it; a PR waiting on its
	// reviewers stays a pill, so the tray never sits over work for nothing.
	let pinned = $state(false);
	const actionable = $derived(
		lanes.some((l) => l.kind === 'staged' || l.kind === 'refused' || l.kind === 'live'),
	);
	const expanded = $derived(tray.open && (pinned || actionable));

	const summary = (c: Change) =>
		c.action === 'change'
			? `${c.field.toLowerCase()} ${c.from} to ${c.to}`
			: c.action === 'add'
				? `${c.field.toLowerCase()}${c.to ? ` ${c.to}` : ''}`
				: `remove ${c.field.toLowerCase()}`;
	const itemLine = (it: DraftItem) =>
		it.kind === 'delete'
			? 'delete'
			: it.changes.slice(0, 2).map(summary).join(', ') +
				(it.changes.length > 2 ? `, +${it.changes.length - 2} more` : '');

	function pillText(l: TrayLane): string {
		switch (l.kind) {
			case 'staged':
				return `${l.draft.count} staged in ${l.project}`;
			case 'review':
				return `PR #${l.pr.prNumber} in review`;
			case 'applying':
				return `Applying PR #${l.pr.prNumber}`;
			case 'live':
				return `PR #${l.pr.prNumber} is live`;
			case 'refused':
				return `PR #${l.pr.prNumber} refused`;
			default:
				return `PR #${l.pr.prNumber} merged`;
		}
	}
	const worst = $derived(lanes.find((l) => l.kind === 'refused') ?? lanes[0]);
</script>

{#if !hidden}
	<aside
		aria-label="Changes in flight"
		class="pointer-events-none absolute right-4 bottom-4 z-20 flex w-[25rem] max-w-[calc(100%-2rem)] flex-col items-end gap-2"
	>
		{#if expanded}
			<div class="pointer-events-auto flex max-h-[65vh] w-full flex-col gap-2 overflow-y-auto">
				{#each lanes as lane (lane.key)}
					<section
						class="overflow-hidden rounded-lg border bg-panel text-xs shadow-lg {lane.kind ===
						'refused'
							? 'border-danger/40'
							: lane.kind === 'live'
								? 'border-ok/40'
								: 'border-line-strong'}"
					>
						<header
							class="flex items-center gap-2 border-b border-line-soft px-3 py-2 {lane.kind ===
							'refused'
								? 'bg-danger-soft'
								: lane.kind === 'live'
									? 'bg-ok-soft'
									: ''}"
						>
							{#if lane.kind === 'staged'}
								<GitBranch size={14} class="text-accent-ink" />
								<strong class="text-sm text-ink"
									>{lane.draft.count} staged change{lane.draft.count === 1 ? '' : 's'}</strong
								>
							{:else if lane.kind === 'review'}
								<GitPullRequest size={14} class="text-accent-ink" />
								<strong class="text-sm text-ink">PR #{lane.pr.prNumber} open</strong>
							{:else if lane.kind === 'applying'}
								<LoaderCircle size={14} class="animate-spin text-accent-ink" />
								<strong class="text-sm text-ink">PR #{lane.pr.prNumber} merged</strong>
							{:else if lane.kind === 'live'}
								<CircleCheck size={14} class="text-ok" />
								<strong class="text-sm text-ok-ink">Live on the cluster</strong>
							{:else if lane.kind === 'refused'}
								<TriangleAlert size={14} class="text-danger-ink" />
								<strong class="text-sm text-danger-ink"
									>The cluster refused PR #{lane.pr.prNumber}</strong
								>
							{:else}
								<GitPullRequest size={14} class="text-ok" />
								<strong class="text-sm text-ink">PR #{lane.pr.prNumber} merged</strong>
							{/if}
							<span class="truncate text-ink-muted">in {lane.project}</span>
						</header>

						{#if lane.kind === 'staged'}
							<ul class="divide-y divide-line-soft">
								{#each lane.draft.items.slice(0, 4) as it (it.kind + it.namespace + it.name)}
									<li class="flex items-center gap-2 px-3 py-1.5">
										<span class="min-w-0 flex-1 truncate"
											><strong class="font-medium text-ink">{it.name}</strong>
											<span class="text-ink-soft">{itemLine(it)}</span></span
										>
										{#if it.changes.some((c) => c.restart)}
											<span class="shrink-0 rounded-full px-1.5 {TONE_PILL.warn}">Restart</span>
										{/if}
									</li>
								{/each}
							</ul>
							{#if lane.draft.items.length > 4}
								<p class="px-3 pb-1 text-ink-muted">and {lane.draft.items.length - 4} more</p>
							{/if}
							{#if lane.draft.prune?.length}
								<p class="border-t border-line-soft px-3 py-1.5 {TONE_TEXT.danger}">
									Merging also deletes {lane.draft.prune.length} object{lane.draft.prune.length ===
									1
										? ''
										: 's'} git no longer declares.
								</p>
							{/if}
							{#each lane.others as o (o.prNumber)}
								<p class="border-t border-line-soft bg-inset px-3 py-1.5 text-ink-soft">
									Also open in {lane.project}:
									<a class="text-accent-ink hover:underline" href={o.prURL} target="_blank"
										>PR #{o.prNumber}</a
									>{o.by ? ` by ${o.by}` : ''}
								</p>
							{/each}
							<footer class="flex items-center gap-3 border-t border-line-soft px-3 py-2">
								<Button
									size="sm"
									onclick={() => (ui.modal = { kind: 'propose', project: lane.project })}
									>Review and propose</Button
								>
								<a class="text-ink-soft hover:underline" href={changesHref(lane.project)}
									>Open in Changes</a
								>
							</footer>
						{:else if lane.kind === 'review'}
							{@const checks = checksPill(lane.pr)}
							{@const approvals = approvalLine(lane.pr)}
							<div class="space-y-1 px-3 py-2">
								{#if lane.pr.title}<p class="truncate text-ink">{lane.pr.title}</p>{/if}
								{#if checks || approvals}
									<p class="flex flex-wrap gap-1.5">
										{#if checks}<span class="rounded-full px-1.5 {TONE_PILL[checks.tone]}"
												>{checks.text}</span
											>{/if}
										{#if approvals}<span class="rounded-full px-1.5 {TONE_PILL[approvals.tone]}"
												>{approvals.text}</span
											>{/if}
									</p>
								{/if}
								<p class="text-ink-soft">
									Next: someone with merge rights approves and merges it in the forge.
								</p>
							</div>
							<footer class="flex items-center gap-3 border-t border-line-soft px-3 py-2">
								<Button size="sm" variant="secondary" href={lane.pr.prURL} target="_blank"
									>Open PR #{lane.pr.prNumber}</Button
								>
								<a
									class="text-ink-soft hover:underline"
									href={reviewURL({
										kind: 'proposal',
										project: lane.project,
										prNumber: lane.pr.prNumber,
									})}>Review in Changes</a
								>
							</footer>
						{:else if lane.kind === 'applying'}
							<p class="px-3 py-2 text-ink-soft">
								{#if lane.pr.title}<span class="text-ink">{lane.pr.title}.</span>{/if}
								Applying to the cluster. Nothing to do; this updates as the cluster moves.
							</p>
						{:else if lane.kind === 'live'}
							<div class="flex items-center gap-3 px-3 py-2">
								<p class="min-w-0 flex-1 text-ink-soft">
									PR #{lane.pr.prNumber}{lane.pr.title ? ` ${lane.pr.title}` : ''} runs as git main declares.
								</p>
								<button class="text-ink-soft hover:underline" onclick={() => tray.dismiss(lane.key)}
									>Dismiss</button
								>
							</div>
						{:else if lane.kind === 'refused'}
							<div class="space-y-1 px-3 py-2">
								<p class="text-ink-soft">What ran before keeps running.</p>
								{#if lane.error}<p
										class="truncate font-mono text-[11px] text-ink-muted"
										title={lane.error}
									>
										{lane.error}
									</p>{/if}
							</div>
							<footer class="flex items-center gap-3 border-t border-line-soft px-3 py-2">
								<Button
									size="sm"
									variant="secondary"
									href={lane.project === PLATFORM_PROJECT
										? '/changes'
										: hrefForScope({ kind: 'project', project: lane.project })}>See why</Button
								>
								<a class="text-ink-soft hover:underline" href={changesHref(lane.project)}
									>Undo in Changes</a
								>
								<button
									class="ml-auto text-ink-soft hover:underline"
									onclick={() => tray.dismiss(lane.key)}>Dismiss</button
								>
							</footer>
						{:else}
							<div class="flex items-center gap-3 px-3 py-2">
								<p class="min-w-0 flex-1 text-ink-soft">
									GitOps status is not available, so dotvirt cannot follow it to the cluster.
								</p>
								<button class="text-ink-soft hover:underline" onclick={() => tray.dismiss(lane.key)}
									>Dismiss</button
								>
							</div>
						{/if}
					</section>
				{/each}
			</div>
			<button
				class="pointer-events-auto flex items-center gap-1 rounded-full border border-line-strong bg-panel px-3 py-1 text-xs text-ink-soft shadow hover:bg-inset"
				onclick={() => {
					tray.open = false;
					pinned = false;
				}}
			>
				<ChevronDown size={12} /> Fold
			</button>
		{:else}
			<button
				onclick={() => {
					tray.open = true;
					pinned = true;
				}}
				class="pointer-events-auto flex items-center gap-2 rounded-full border bg-panel py-1.5 pr-1.5 pl-3 text-xs font-medium text-ink shadow-lg {worst.kind ===
				'refused'
					? 'border-danger/50'
					: 'border-accent'}"
			>
				{#if worst.kind === 'refused'}<TriangleAlert
						size={13}
						class="text-danger-ink"
					/>{:else}<GitBranch size={13} class="text-accent-ink" />{/if}
				{pillText(worst)}{lanes.length > 1 ? `, +${lanes.length - 1}` : ''}
				<span class="rounded-full bg-accent px-2.5 py-0.5 text-white">Show</span>
			</button>
		{/if}
	</aside>
{/if}
