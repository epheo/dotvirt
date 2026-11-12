<script lang="ts">
	import { branding } from '#lib/state/branding.svelte.js';
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import {
		ChevronDown,
		Circle,
		CircleCheck,
		GitBranch,
		GitPullRequest,
		LoaderCircle,
		TriangleAlert,
	} from '@lucide/svelte';
	import { ACCESS_UNKNOWN } from '#lib/authoring.js';
	import { changesHref, hrefForScope } from '#lib/nav.js';
	import { approvalLine, checksPill, reviewURL } from '#lib/review.js';
	import { TONE_PILL, TONE_TEXT } from '#lib/status.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { drafts, PLATFORM_PROJECT } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { itemLine, trayLanes, type TrayLane } from '#lib/tray.js';
	import Button from './Button.svelte';
	import DiscardDraft from './DiscardDraft.svelte';
	import ForgeAdminCopy from './ForgeAdminCopy.svelte';

	// The one place a change of this user's is followed from staged to live:
	// it replaces the header Propose button, the Changes dot and the pending
	// banners. Every lane names what happens next and who acts. Hidden on
	// /changes, which is the full view of the same pipeline.
	// A merge is placed against its project's whole history. A caller refused
	// it (a platform stager without platform read, a viewer of part of a
	// project) gets no verdict here, and no read is sent: their merge cannot
	// be followed past git. Until the answer lands nothing is read either.
	const reads = (p: string) => inventory.repoRead(p).ok;
	const unfollowable = (p: string) => {
		const g = inventory.repoRead(p);
		return !g.ok && g.why !== CHECKING && g.why !== ACCESS_UNKNOWN;
	};
	const syncOf = (p: string) =>
		unfollowable(p)
			? undefined
			: p === PLATFORM_PROJECT
				? inventory.inventory?.platform
				: inventory.projects.find((x) => x.name === p)?.gitOps;
	const lanes = $derived(
		trayLanes({
			drafts: drafts.drafts,
			proposals: inventory.proposals,
			followed: tray.followed,
			sync: syncOf,
			commits: (p) => tray.commits(p),
		}),
	);

	// A merge Argo has not picked up may already be held by a newer revision
	// than the last history read knows: each revision move reads again.
	const waitingKey = $derived(
		lanes
			.flatMap((l) =>
				l.kind === 'applying' && !l.picked && reads(l.project)
					? [`${l.project}@${syncOf(l.project)?.revision ?? ''}`]
					: [],
			)
			.sort()
			.join(' '),
	);
	$effect(() => {
		if (!waitingKey) return;
		untrack(() => {
			for (const k of waitingKey.split(' ')) tray.want(k.slice(0, k.lastIndexOf('@')));
		});
	});

	// Follow on the stable keys, never the per-frame arrays, and only once the
	// first frame has landed: before it the open list is empty, not answered.
	const streamed = $derived(inventory.inventory !== null);
	$effect(() => {
		inventory.proposalsKey;
		inventory.taskFeed;
		if (streamed) untrack(() => tray.update(inventory.proposals, inventory.taskFeed, reads));
	});

	// A live merge has said what it had to; it leaves by itself, with the
	// countdown in view, and waits while the pointer rests on it.
	const LIVE_MS = 15_000;
	const liveKeys = $derived(
		lanes
			.filter((l) => l.kind === 'live')
			.map((l) => l.key)
			.join(' '),
	);
	let now = $state(Date.now());
	let held = $state<string | null>(null);
	const since = new Map<string, number>();
	$effect(() => {
		const keys = liveKeys ? liveKeys.split(' ') : [];
		if (!keys.length) return;
		const t = setInterval(() => {
			now = Date.now();
			for (const k of keys) {
				const at = since.get(k);
				if (at === undefined || k === held) since.set(k, now);
				else if (now - at >= LIVE_MS) {
					since.delete(k);
					tray.dismiss(k);
				}
			}
		}, 1000);
		return () => clearInterval(t);
	});
	const closesIn = (key: string) =>
		Math.max(1, Math.ceil((LIVE_MS - (now - (since.get(key) ?? now))) / 1000));

	// Lanes asking to discard their draft, each on its own.
	let discarding = $state<Record<string, boolean>>({});

	async function copyLink(pr: { prNumber: number; prURL: string }) {
		try {
			await navigator.clipboard.writeText(pr.prURL);
			ui.showToast(`Link to PR #${pr.prNumber} copied. Send it to a reviewer.`, {
				kind: 'success',
			});
		} catch {
			ui.toastError(`Could not copy. PR #${pr.prNumber} is at ${pr.prURL}`);
		}
	}

	// Undo is a revert PR of the merge commit, the project's own undo verb.
	function undo(project: string, hash: string) {
		reviewCache.loadCommit(project, hash);
		ui.modal = { kind: 'undo', project, hash };
	}

	type Step = { text: string; state: 'done' | 'now' | 'todo' };
	const applySteps = (picked: boolean, restarts: string[] = []): Step[] => [
		{ text: 'Git main updated', state: 'done' },
		{ text: 'Argo CD picked up the merge', state: picked ? 'done' : 'now' },
		{
			text: restarts.length
				? `Applying; ${restarts.join(', ')} restart${restarts.length === 1 ? 's' : ''} once`
				: 'Applying to the cluster',
			state: picked ? 'now' : 'todo',
		},
	];

	const hidden = $derived(page.url.pathname.startsWith('/changes') || lanes.length === 0);

	// A pill while browsing; it unfolds only when the user asks, so it never
	// sits over work by itself. The pill names the lane that matters most.
	const expanded = $derived(tray.open);

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
	function unfold() {
		tray.open = true;
	}
	const RANK: Record<TrayLane['kind'], number> = {
		refused: 0,
		staged: 1,
		live: 2,
		applying: 3,
		review: 4,
		merged: 5,
	};
	const worst = $derived([...lanes].sort((a, b) => RANK[a.kind] - RANK[b.kind])[0]);
</script>

{#if !hidden}
	<!-- Lives at the dock bar's right end; unfolded lanes float above the bar. -->
	<aside aria-label="Changes in flight" class="relative flex items-center">
		{#if expanded}
			<div
				class="absolute right-0 bottom-full z-20 mb-2 flex max-h-[65vh] w-[25rem] max-w-[calc(100vw-2rem)] flex-col gap-2 overflow-y-auto"
			>
				{#each lanes as lane (lane.key)}
					<section
						aria-label={lane.kind === 'live' ? `PR #${lane.pr.prNumber} is live` : undefined}
						role={lane.kind === 'live' ? 'group' : undefined}
						onpointerenter={() => (held = lane.kind === 'live' ? lane.key : null)}
						onpointerleave={() => (held = null)}
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
										{:else if it.kind === 'edit'}
											<span class="shrink-0 text-ink-muted">No restart</span>
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
								{#if discarding[lane.project]}
									<DiscardDraft
										project={lane.project}
										count={lane.draft.count}
										onclose={() => delete discarding[lane.project]}
									/>
								{:else}
									<Button
										size="sm"
										onclick={() => (ui.modal = { kind: 'propose', project: lane.project })}
										>Review and propose</Button
									>
									<button
										class="text-ink-soft hover:underline"
										onclick={() => (discarding[lane.project] = true)}>Discard</button
									>
									<a class="ml-auto text-ink-soft hover:underline" href={changesHref(lane.project)}
										>Open in Changes</a
									>
								{/if}
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
									<ForgeAdminCopy />
								</p>
							</div>
							<footer class="flex items-center gap-3 border-t border-line-soft px-3 py-2">
								<Button size="sm" variant="secondary" href={lane.pr.prURL} target="_blank"
									>Open PR #{lane.pr.prNumber}</Button
								>
								<button class="text-ink-soft hover:underline" onclick={() => copyLink(lane.pr)}
									>Copy link for a reviewer</button
								>
								<a
									class="ml-auto text-ink-soft hover:underline"
									href={reviewURL({
										kind: 'proposal',
										project: lane.project,
										prNumber: lane.pr.prNumber,
									})}>Review in Changes</a
								>
							</footer>
						{:else if lane.kind === 'applying'}
							<div class="space-y-1.5 px-3 py-2">
								{#if lane.pr.title}<p class="truncate text-ink">{lane.pr.title}</p>{/if}
								<ol class="space-y-1">
									{#each applySteps(!!lane.picked, lane.pr.restarts) as st (st.text)}
										<li
											class="flex items-center gap-2 {st.state === 'todo'
												? 'text-ink-muted'
												: 'text-ink'}"
										>
											{#if st.state === 'done'}<CircleCheck
													size={13}
													class="shrink-0 text-ok-ink"
												/>{:else if st.state === 'now'}<LoaderCircle
													size={13}
													class="shrink-0 animate-spin text-accent-ink"
												/>{:else}<Circle size={13} class="shrink-0 text-line-strong" />{/if}
											{st.text}
										</li>
									{/each}
								</ol>
								<p class="text-ink-muted">Nothing to do. This updates as the cluster moves.</p>
							</div>
						{:else if lane.kind === 'live'}
							<div class="space-y-1 px-3 py-2">
								{#if lane.pr.lines?.length}
									<p class="text-ink-soft">Running as git main declares:</p>
									<ul class="space-y-0.5 text-ink">
										{#each lane.pr.lines.slice(0, 4) as l (l)}<li class="truncate">{l}</li>{/each}
									</ul>
								{:else}
									<p class="text-ink-soft">
										PR #{lane.pr.prNumber}{lane.pr.title ? ` ${lane.pr.title}` : ''} runs as git main
										declares.
									</p>
								{/if}
							</div>
							<footer class="flex items-center gap-3 border-t border-line-soft px-3 py-2">
								{#if lane.pr.hash && inventory.canRevert(lane.project)}
									<Button
										size="sm"
										variant="secondary"
										onclick={() => undo(lane.project, lane.pr.hash ?? '')}>Undo this change</Button
									>
								{/if}
								<button class="text-ink-soft hover:underline" onclick={() => tray.dismiss(lane.key)}
									>Dismiss</button
								>
								<span class="ml-auto text-ink-muted tabular-nums"
									>{held === lane.key
										? 'Stays while you look'
										: `Closes in ${closesIn(lane.key)} s`}</span
								>
							</footer>
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
										: hrefForScope({ kind: 'project', project: lane.project })}
									>Fix and propose again</Button
								>
								{#if lane.pr.hash && inventory.canRevert(lane.project)}
									<button
										class="text-ink-soft hover:underline"
										onclick={() => undo(lane.project, lane.pr.hash ?? '')}>Undo the merge</button
									>
								{:else if !lane.pr.hash && inventory.repoRead(lane.project).ok}
									<a class="text-ink-soft hover:underline" href={changesHref(lane.project)}
										>Undo in Changes</a
									>
								{/if}
								<button
									class="ml-auto text-ink-soft hover:underline"
									onclick={() => tray.dismiss(lane.key)}>Dismiss</button
								>
							</footer>
						{:else}
							<div class="flex items-center gap-3 px-3 py-2">
								<p class="min-w-0 flex-1 text-ink-soft">
									{!unfollowable(lane.project)
										? `GitOps status is not available, so ${branding.name} cannot follow it to the cluster.`
										: lane.project === PLATFORM_PROJECT
											? `You cannot read the platform's history, so ${branding.name} cannot follow it to the cluster.`
											: `You cannot read all of ${lane.project}, so ${branding.name} cannot follow it to the cluster.`}
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
				class="flex h-7 items-center gap-1 rounded-full border border-line-strong bg-panel px-3 text-xs text-ink-soft hover:bg-inset"
				onclick={() => {
					tray.open = false;
				}}
			>
				<ChevronDown size={12} /> Fold
			</button>
		{:else}
			<!-- A staged draft never hides: its pill opens the review sheet in one click. -->
			<div
				class="flex h-7 items-center gap-2 rounded-full border bg-panel pr-1 pl-2.5 text-xs font-medium text-ink shadow-[0_2px_8px_rgba(0,0,0,.12)] {worst.kind ===
				'refused'
					? 'border-danger/50'
					: 'border-accent'}"
			>
				<button class="flex items-center gap-2" onclick={unfold}>
					{#if worst.kind === 'refused'}<TriangleAlert
							size={13}
							class="text-danger-ink"
						/>{:else}<GitBranch size={13} class="text-accent-ink" />{/if}
					{pillText(worst)}{lanes.length > 1 ? `, +${lanes.length - 1}` : ''}
				</button>
				{#if worst.kind === 'staged'}
					{@const project = worst.project}
					<button
						class="inline-flex h-5 items-center rounded-full bg-accent px-2.5 font-bold text-white"
						onclick={() => (ui.modal = { kind: 'propose', project })}>Review</button
					>
				{:else}
					<button
						class="inline-flex h-5 items-center rounded-full bg-accent px-2.5 font-bold text-white"
						onclick={unfold}>Show</button
					>
				{/if}
			</div>
		{/if}
	</aside>
{/if}
