<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Proposal, type ProposeResult } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { changesHref } from '$lib/nav';
	import {
		commitKey,
		itemKey,
		parseReview,
		prKey,
		reviewURL,
		sameReview,
		type ReviewSel,
	} from '$lib/review';
	import { drafts, PLATFORM_PROJECT } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { persisted } from '$lib/state/persisted.svelte';
	import { reviewCache } from '$lib/state/reviewCache.svelte';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import RepoBanner from '$lib/components/RepoBanner.svelte';
	import ChangesLanes, { type HistoryLane } from './ChangesLanes.svelte';
	import ReviewCommit from './ReviewCommit.svelte';
	import ReviewItem from './ReviewItem.svelte';
	import ReviewProposal from './ReviewProposal.svelte';
	import UndoModal from './UndoModal.svelte';

	// The Changes section's workspace: everything the GitOps write model has in
	// flight, and what already landed, for the scope the tree picked. Left, the
	// lifecycle: staged drafts (per project), every open PR of the caller's
	// projects, history. Right: the selected change's field diff and impact,
	// the selected PR's diff and review state, or a past commit's diff with its
	// revert. Review happens HERE; approval and merge stay in the forge - the
	// primary actions are Propose and Undo, both of which open a pull request.
	//
	// Staged and Proposed are project-grained (a draft is a branch, a proposal
	// is a PR), so a namespace scope still shows its project's lanes; only
	// History narrows to the namespace. A VM's own history lives on the VM
	// page's Changes tab, with Restore as the object-level undo.
	let {
		project: scopeProject = '',
		namespace: scopeNS = '',
	}: { project?: string; namespace?: string } = $props();

	// Warning-only lanes stay: prune risk must warn BEFORE anything is staged.
	const lanes = $derived(
		drafts.drafts.filter(
			(d) =>
				(d.draft.count > 0 || d.draft.warning) && (!scopeProject || d.project === scopeProject),
		),
	);
	const proposals = $derived(
		scopeProject
			? inventory.proposals.filter((p) => p.project === scopeProject)
			: inventory.proposals,
	);
	// Repo-backed projects, for History (platform included for authors); one
	// project under a scope.
	const repoProjects = $derived.by(() => {
		const all = inventory.canManage
			? [...inventory.repoProjects, PLATFORM_PROJECT]
			: inventory.repoProjects;
		return scopeProject ? all.filter((p) => p === scopeProject) : all;
	});
	const scopeTracked = $derived(!scopeProject || repoProjects.length > 0);
	const trail = $derived.by(() => {
		const t: { label: string; href?: string }[] = [
			{ label: 'All changes', href: scopeProject ? '/changes' : undefined },
		];
		if (scopeProject)
			t.push({ label: scopeProject, href: scopeNS ? changesHref(scopeProject) : undefined });
		if (scopeNS) t.push({ label: scopeNS });
		return t;
	});

	// History per repo-backed project, loaded when its lane opens. Kept here,
	// not in the lanes: a selected commit resolves against it.
	let history = $state<Record<string, HistoryLane>>({});
	function openHistory(project: string) {
		if (!history[project]) history[project] = { open: true, busy: false, error: '', commits: [] };
		history[project].open = true;
		loadHistory(project);
	}
	function toggleHistory(project: string) {
		if (history[project]?.open) history[project].open = false;
		else openHistory(project);
	}
	async function loadHistory(project: string) {
		const lane = history[project];
		lane.busy = true;
		lane.error = '';
		try {
			lane.commits = await api.history(project, scopeNS || undefined);
		} catch (e) {
			lane.error = friendlyError(e);
		} finally {
			lane.busy = false;
		}
	}
	// A scope opens its history without a click; a namespace narrows it.
	$effect(() => {
		const p = scopeProject;
		scopeNS;
		if (!p) return;
		untrack(() => openHistory(p));
	});

	// The selection mirrors the URL (see review.ts), so a review is shareable
	// and the VM page deep-links a commit. Written with replaceState, like tabs:
	// back never walks selection moves. Navigation drives the state back, so a
	// deep link or an in-app goto selects without a click.
	let sel = $state<ReviewSel | null>(parseReview(page.url.searchParams));
	$effect(() => {
		const q = page.url.searchParams;
		const next = parseReview(q);
		const project = q.get('project');
		untrack(() => {
			if (!sameReview(next, sel)) sel = next;
			// ?project= alone, or with a commit, is a request for that history.
			if (project && (!next || next.kind === 'commit') && !history[project]?.open)
				openHistory(project);
		});
	});
	// A selection keeps the scope: the query moves, the path (all, project,
	// namespace) stays where the tree put it.
	function select(s: ReviewSel) {
		sel = s;
		const url = reviewURL(s);
		goto(page.url.pathname + url.slice(url.indexOf('?')), {
			replaceState: true,
			keepFocus: true,
			noScroll: true,
		});
	}

	// Propose results outlive their lane (it empties); rendered until the live
	// stream carries the PR, exactly like the drawer used to.
	let results = $state<Record<string, ProposeResult & { title?: string }>>({});
	// The PR lane: streamed proposals plus just-proposed results the stream has
	// not carried yet - same rendering, so the stream takes over invisibly.
	const proposedLane = $derived.by((): Proposal[] => {
		const out: Proposal[] = [...proposals];
		const seen = new Set(out.map((p) => prKey(p.project, p.prNumber)));
		for (const [project, r] of Object.entries(results)) {
			if (!r.prURL || !r.prNumber || seen.has(prKey(project, r.prNumber))) continue;
			out.push({ project, prNumber: r.prNumber, prURL: r.prURL, title: r.title ?? '', mine: true });
		}
		return out.sort((a, b) => a.project.localeCompare(b.project));
	});
	$effect(() => {
		for (const p of proposals) {
			if (untrack(() => results[p.project])?.prNumber === p.prNumber) delete results[p.project];
		}
	});
	// A propose moves the project's open PR head, so its cached review is stale.
	function proposed(project: string, r: ProposeResult & { title?: string }) {
		results[project] = r;
		reviewCache.forgetProposals(project);
	}
	// The lane is the project's, not the caller's: a colleague's PR is as much
	// in flight as one's own. The filter is a viewing preference, kept.
	const mineOnly = persisted('dotvirt.changes.mine', false);
	const visibleProposals = $derived(
		mineOnly.value ? proposedLane.filter((p) => p.mine) : proposedLane,
	);

	// Resolve the selection against live data; fall back to the first staged
	// item, then the first PR (drafts and PRs both move under the page). A
	// commit stays selected before its history row has loaded: the review
	// itself names it once the detail lands.
	const selected = $derived.by(() => {
		if (sel?.kind === 'item') {
			const lane = lanes.find((l) => l.project === sel!.project);
			const item = lane?.draft.items.find((it) => itemKey(it) === (sel as { key: string }).key);
			if (lane && item) return { kind: 'item' as const, project: lane.project, item };
		}
		if (sel?.kind === 'proposal') {
			const p = proposedLane.find(
				(p) => p.project === sel!.project && p.prNumber === (sel as { prNumber: number }).prNumber,
			);
			if (p) return { kind: 'proposal' as const, proposal: p };
		}
		if (sel?.kind === 'commit') {
			const { project, hash } = sel;
			const commit =
				history[project]?.commits.find((c) => c.hash === hash) ??
				reviewCache.commit(project, hash)?.data?.commit ??
				null;
			return { kind: 'commit' as const, project, hash, commit };
		}
		const withItems = lanes.find((l) => l.draft.items.length > 0);
		const first = withItems?.draft.items[0];
		if (withItems && first)
			return { kind: 'item' as const, project: withItems.project, item: first };
		if (visibleProposals[0]) return { kind: 'proposal' as const, proposal: visibleProposals[0] };
		return null;
	});
	// The resolved selection as the lanes highlight it.
	const active = $derived.by((): ReviewSel | null => {
		if (!selected) return null;
		if (selected.kind === 'item')
			return { kind: 'item', project: selected.project, key: itemKey(selected.item) };
		if (selected.kind === 'proposal')
			return {
				kind: 'proposal',
				project: selected.proposal.project,
				prNumber: selected.proposal.prNumber,
			};
		return { kind: 'commit', project: selected.project, hash: selected.hash };
	});
	const selectedVM = $derived.by(() => {
		if (selected?.kind !== 'item') return null;
		const { item } = selected;
		if (item.resource && item.resource !== 'vm') return null;
		return inventory.findVM(item.namespace, item.name);
	});

	// A revert's outcome stays with its commit for the session, so the pane
	// keeps pointing at the PR.
	let reverts = $state<Record<string, ProposeResult>>({});
	let confirmUndo = $state<{ project: string; hash: string } | null>(null);

	onMount(() => {
		untrack(() => drafts.refresh());
	});

	const stagedTotal = $derived(lanes.reduce((n, l) => n + l.draft.count, 0));
	const laneOf = (project: string) => lanes.find((l) => l.project === project)?.draft;
</script>

<svelte:head><title>Changes — dotvirt</title></svelte:head>

<div class="flex min-h-0 flex-1 flex-col">
	<div class="flex items-center border-b border-line">
		<div class="min-w-0 flex-1 [&>div]:border-b-0">
			<Breadcrumb {trail} />
		</div>
		<span class="shrink-0 px-4 text-xs text-ink-faint">
			{stagedTotal} staged · {proposedLane.length} proposed. Review here; approve and merge in the forge.
		</span>
	</div>
	{#if scopeProject && !scopeTracked}
		<RepoBanner project={scopeProject} />
	{/if}

	<div class="flex min-h-0 flex-1">
		<ChangesLanes
			{scopeProject}
			{lanes}
			{proposedLane}
			{visibleProposals}
			{mineOnly}
			{results}
			{history}
			{repoProjects}
			{active}
			onselect={select}
			ontogglehistory={toggleHistory}
			ondismiss={(project) => delete results[project]}
		/>

		<div class="flex min-w-0 flex-1 flex-col overflow-y-auto">
			{#if selected?.kind === 'item'}
				{@const project = selected.project}
				<ReviewItem
					{project}
					item={selected.item}
					lane={laneOf(project)}
					vm={selectedVM}
					onproposed={(r) => proposed(project, r)}
				/>
			{:else if selected?.kind === 'proposal'}
				<ReviewProposal proposal={selected.proposal} />
			{:else if selected?.kind === 'commit'}
				{@const { project, hash } = selected}
				<ReviewCommit
					{project}
					{hash}
					commit={selected.commit}
					done={reverts[commitKey(project, hash)]}
					onundo={() => (confirmUndo = { project, hash })}
				/>
			{:else}
				<div class="flex flex-1 items-center justify-center">
					<div class="max-w-md text-center text-sm text-ink-faint">
						<p class="mb-1 font-medium text-ink-soft">
							Nothing in flight{scopeProject ? ` for ${scopeNS || scopeProject}` : ''}.
						</p>
						<p>
							Every configuration change is staged here first, proposed as a pull request, and
							applied by ArgoCD after the merge — the audit trail is the project's git history.
						</p>
					</div>
				</div>
			{/if}
		</div>
	</div>
</div>

{#if confirmUndo}
	{@const { project, hash } = confirmUndo}
	{@const review = reviewCache.commit(project, hash)?.data}
	<UndoModal
		{project}
		{hash}
		commit={history[project]?.commits.find((c) => c.hash === hash) ?? review?.commit}
		warning={review?.revertWarning}
		onclose={() => (confirmUndo = null)}
		onreverted={(r) => (reverts[commitKey(project, hash)] = r)}
	/>
{/if}
