<script lang="ts">
	import { onMount, untrack, type Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, Unauthorized, type Proposal, type ProposeResult } from '#lib/api.js';
	import { friendlyError, plural } from '#lib/format.js';
	import { clusterGate, namespaceHistoryGate } from '#lib/gates.js';
	import { opStands } from '#lib/gitops.js';
	import { CHANGES_TABS, changesHref, changesTab, type ChangesTab } from '#lib/nav.js';
	import { bySize } from '#lib/rail.js';
	import {
		commitKey,
		itemKey,
		parseReview,
		prKey,
		reviewURL,
		sameReview,
		type ReviewSel,
	} from '#lib/review.js';
	import { caps, CHECKING } from '#lib/state/caps.svelte.js';
	import { drafts, PLATFORM_PROJECT } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Card from '#lib/components/Card.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import RepoBanner from '#lib/components/RepoBanner.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import ChangesLanes, { type HistoryLane } from './ChangesLanes.svelte';
	import ReviewCommit from './ReviewCommit.svelte';
	import ReviewItem from './ReviewItem.svelte';
	import ReviewProposal from './ReviewProposal.svelte';
	import UndoModal from './UndoModal.svelte';

	// The Changes section's workspace: everything the GitOps write model has in
	// flight, and what already landed, for the scope the tree picked. One tab
	// per stage, each a work queue: the list on the left, the selected change
	// on the right. Staged drafts carry the propose form; an open PR, its diff
	// and review state; a past commit, its diff and Undo. Review happens HERE;
	// approval and merge stay in the forge, so the primary actions are Propose
	// and Undo, both of which open a pull request.
	//
	// Staged and In review are project-grained (a draft is a branch, a
	// proposal is a PR), so a namespace scope still shows its project's; only
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
	const failedLanes = $derived(
		drafts.failed.filter((f) => !scopeProject || f.project === scopeProject),
	);
	const proposals = $derived(
		scopeProject
			? inventory.proposals.filter((p) => p.project === scopeProject)
			: inventory.proposals,
	);
	// Repo-backed projects, for History (platform included for its readers),
	// biggest first like the tree; one project under a scope.
	const repoProjects = $derived.by(() => {
		const tracked = bySize(
			inventory.projects.filter((p) => p.repo),
			(p) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0),
			(p) => p.name,
		).map((p) => p.name);
		const all = clusterGate.platformRead().ok ? [...tracked, PLATFORM_PROJECT] : tracked;
		return scopeProject ? all.filter((p) => p === scopeProject) : all;
	});
	const scopeTracked = $derived(!scopeProject || repoProjects.length > 0);

	// History per repo-backed project, loaded when its lane opens. Kept here,
	// not in the lanes: a selected commit resolves against it.
	let history = $state<Record<string, HistoryLane>>({});
	const syncOf = (project: string) =>
		project === PLATFORM_PROJECT
			? inventory.inventory?.platform
			: inventory.projects.find((p) => p.name === project)?.gitOps;
	// A lane reads the project's whole repo, or under a namespace scope that
	// namespace's history.
	const readGate = (project: string) =>
		scopeNS ? namespaceHistoryGate(scopeNS) : inventory.repoRead(project);
	// Reviewing a past change reads the whole repo. Asked of the scope's
	// namespace when there is one: its answer is already held.
	const repoRead = (project: string) =>
		inventory.repoRead(project, project === scopeProject ? scopeNS : '');
	// Nothing opens by default when the first PR in view is another's that
	// only the forge opens.
	const forgeOnly = $derived.by(() => {
		const f = visibleProposals[0];
		if (!f || f.mine) return false;
		const g = repoRead(f.project);
		return !g.ok && g.why !== CHECKING;
	});
	// Under a scope, whether its commits open for review.
	const scopeReads = $derived(scopeProject ? repoRead(scopeProject) : null);
	const scopeRefused = $derived(!!scopeReads && !scopeReads.ok && scopeReads.why !== CHECKING);
	// A lane's content is a function of the namespace scope, the project's
	// applied revision (a merge that reaches the cluster moves it) and whether
	// the caller may read it.
	const laneKey = (project: string) => {
		const g = readGate(project);
		return `${scopeNS}|${syncOf(project)?.revision ?? ''}|${g.ok ? '' : g.why}`;
	};
	// Open lanes only: a closed one asks nothing, its gate included.
	const historyKeys = $derived(
		Object.entries(history)
			.filter(([, l]) => l.open)
			.map(([p]) => `${p}|${laneKey(p)}`)
			.join('\0'),
	);
	// Only a lane's newest load writes: scope switches and revision moves
	// overlap, and a late response would paint another scope's commits.
	let historySeq = 0;
	const latest = new Map<string, { seq: number; key: string }>();
	function openHistory(project: string) {
		if (!history[project])
			history[project] = { open: true, busy: false, error: '', denied: '', commits: [] };
		history[project].open = true;
		loadHistory(project);
	}
	function toggleHistory(project: string) {
		if (history[project]?.open) history[project].open = false;
		else openHistory(project);
	}
	async function loadHistory(project: string) {
		const lane = history[project];
		const key = laneKey(project);
		const seq = ++historySeq;
		// Another namespace's commits must not linger while this scope loads.
		if (latest.get(project)?.key.split('|')[0] !== scopeNS) lane.commits = [];
		latest.set(project, { seq, key });
		const mine = () => latest.get(project)?.seq === seq;
		lane.error = '';
		// Read only once the caller may: the gate's answer re-keys the lane.
		const gate = readGate(project);
		lane.denied = gate.ok || gate.why === CHECKING ? '' : gate.why;
		if (!gate.ok) {
			lane.busy = gate.why === CHECKING;
			lane.commits = [];
			return;
		}
		lane.busy = true;
		try {
			const commits = await api.history(project, scopeNS || undefined);
			if (mine()) lane.commits = commits;
		} catch (e) {
			if (mine() && !(e instanceof Unauthorized)) lane.error = friendlyError(e);
		} finally {
			if (mine()) lane.busy = false;
		}
	}
	// A scope opens its history without a click; a namespace narrows it. A
	// project without a repo has none to read.
	$effect(() => {
		const p = scopeProject;
		scopeNS;
		if (!p || !scopeTracked) return;
		untrack(() => openHistory(p));
	});
	// Open lanes follow their key: a namespace switch or a revision move.
	$effect(() => {
		historyKeys;
		untrack(() => {
			for (const [project, lane] of Object.entries(history))
				if (lane.open && latest.get(project)?.key !== laneKey(project)) loadHistory(project);
		});
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
	// namespace) stays where the tree put it. Its tab follows from its kind.
	function select(s: ReviewSel) {
		sel = s;
		const url = reviewURL(s);
		goto(page.url.pathname + url.slice(url.indexOf('?')), { replace: true, reset: false });
	}

	// Propose results outlive their lane (it empties); rendered until the live
	// stream carries the PR.
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
	// Keyed on the PR-set string, not the per-frame proposals array.
	$effect(() => {
		inventory.proposalsKey;
		untrack(() => {
			for (const p of inventory.proposals)
				if (results[p.project]?.prNumber === p.prNumber) delete results[p.project];
		});
	});
	// A proposed draft is now a PR waiting in the forge: In review opens on it.
	function proposed(project: string, r: ProposeResult & { title?: string }) {
		results[project] = r;
		if (r.prNumber) select({ kind: 'proposal', project, prNumber: r.prNumber });
		else goto(`${page.url.pathname}?tab=review`, { replace: true, reset: false });
	}
	// The lane is the project's, not the caller's: a colleague's PR is as much
	// in flight as one's own. The filter is a viewing preference, kept.
	const mineOnly = persisted('dotvirt.changes.mine', false);
	const visibleProposals = $derived(
		mineOnly.value ? proposedLane.filter((p) => p.mine) : proposedLane,
	);
	const pushed = $derived(Object.values(results).filter((r) => !r.prURL).length);

	const stagedTotal = $derived(lanes.reduce((n, l) => n + l.draft.count, 0));
	const stagedProjects = $derived(lanes.filter((l) => l.draft.count > 0).length);

	// The tab: ?tab= when it names one, else the selection's kind, else what
	// waits on the user (a draft first, then a PR), else a scope's history.
	const tabOf = (s: ReviewSel | null): ChangesTab | null =>
		s?.kind === 'item'
			? 'staged'
			: s?.kind === 'proposal'
				? 'review'
				: s?.kind === 'commit'
					? 'history'
					: null;
	const tab = $derived.by((): ChangesTab => {
		const q = page.url.searchParams;
		const picked = changesTab(q.get('tab')) ?? tabOf(sel);
		if (picked) return picked;
		if (q.get('project')) return 'history';
		if (!drafts.loaded || drafts.error || lanes.length > 0 || failedLanes.length > 0)
			return 'staged';
		if (proposedLane.length > 0 || pushed > 0) return 'review';
		return scopeProject ? 'history' : 'staged';
	});
	const tabLabel = $derived(CHANGES_TABS.find((t) => t.id === tab)!.label);
	const tabs = $derived(
		CHANGES_TABS.map((t) => ({
			id: t.id,
			label: t.label,
			count:
				t.id === 'staged'
					? stagedTotal
					: t.id === 'review'
						? proposedLane.length
						: scopeTracked &&
							  history[scopeProject] &&
							  !history[scopeProject].busy &&
							  !history[scopeProject].denied
							? history[scopeProject].commits.length
							: undefined,
		})),
	);

	// Resolve the selection against live data, within the tab: a staged item
	// falls back to the first one, a PR to the first in view (drafts and PRs
	// both move under the page). A commit stays selected before its history
	// row has loaded: the review itself names it once the detail lands.
	const selected = $derived.by(() => {
		if (tab === 'staged') {
			if (sel?.kind === 'item') {
				const lane = lanes.find((l) => l.project === sel!.project);
				const item = lane?.draft.items.find((it) => itemKey(it) === (sel as { key: string }).key);
				if (lane && item) return { kind: 'item' as const, project: lane.project, item };
			}
			const withItems = lanes.find((l) => l.draft.items.length > 0);
			const first = withItems?.draft.items[0];
			if (withItems && first)
				return { kind: 'item' as const, project: withItems.project, item: first };
			return null;
		}
		if (tab === 'review') {
			if (sel?.kind === 'proposal') {
				const p = proposedLane.find(
					(p) =>
						p.project === sel!.project && p.prNumber === (sel as { prNumber: number }).prNumber,
				);
				if (p) return { kind: 'proposal' as const, proposal: p };
			}
			// The first in view, unless only the forge opens it: then the caller's own.
			const first = visibleProposals[0];
			const open =
				first && (first.mine || repoRead(first.project).ok)
					? first
					: visibleProposals.find((p) => p.mine);
			if (open) return { kind: 'proposal' as const, proposal: open };
			return null;
		}
		if (sel?.kind === 'commit') {
			const { project, hash } = sel;
			const commit =
				history[project]?.commits.find((c) => c.hash === hash) ??
				reviewCache.commit(project, hash)?.data?.commit ??
				null;
			return { kind: 'commit' as const, project, hash, commit };
		}
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

	// A revert's outcome stays with its commit for the session, so the card
	// keeps pointing at the PR.
	let reverts = $state<Record<string, ProposeResult>>({});
	let confirmUndo = $state<{ project: string; hash: string } | null>(null);

	onMount(() => {
		untrack(() => drafts.refresh());
	});

	const laneOf = (project: string) => lanes.find((l) => l.project === project)?.draft;

	// A merge the cluster refused waits on someone: fix it with a new PR or
	// undo it from History. The PR is named only when the tray followed exactly
	// one merge there: with several, which one broke is not known.
	const refused = $derived(
		repoProjects
			.filter((p) => opStands(syncOf(p)))
			.map((project) => {
				const merged = tray.followed.filter((f) => f.project === project && f.mergedAt);
				return { project, prNumber: merged.length === 1 ? merged[0].prNumber : undefined };
			}),
	);
	const historyHref = (project: string) => `${changesHref(project)}?tab=history`;

	const title = $derived(scopeNS || scopeProject || 'Changes');
	const trail = $derived.by(() => {
		const t: { label: string; href?: string }[] = [{ label: 'Changes', href: '/changes' }];
		if (scopeProject)
			t.push({ label: scopeProject, href: scopeNS ? changesHref(scopeProject) : undefined });
		if (scopeNS) t.push({ label: scopeNS });
		if (!scopeProject) t.push({ label: tabLabel });
		return t;
	});
	const context = $derived(
		[
			`${stagedTotal.toLocaleString('en-US')} staged, ${proposedLane.length.toLocaleString('en-US')} in review`,
			scopeNS
				? `in ${scopeProject}`
				: scopeProject
					? ''
					: plural(inventory.projects.length, 'project'),
		]
			.filter(Boolean)
			.join(', '),
	);
	type Status = { tone: 'danger' | 'info'; text: string | Snippet };
	const status = $derived.by((): Status | undefined => {
		if (refused.length) return { tone: 'danger', text: refusedLine };
		if (failedLanes.length)
			return { tone: 'danger', text: `Could not read ${plural(failedLanes.length, 'draft')}` };
		if (stagedTotal > 0)
			return {
				tone: 'info',
				text: `${plural(stagedTotal, 'change')} not proposed${stagedProjects > 1 ? ` in ${plural(stagedProjects, 'project')}` : ''}`,
			};
		return undefined;
	});

	// Which list is empty decides between the queue (list and review) and one
	// card that says what the tab holds and where to go instead.
	// A stager without platform read reaches the platform scope through their
	// own PRs; its history stays the readers'.
	const platformHidden = $derived(
		scopeProject === PLATFORM_PROJECT && caps.cluster?.platformRead === false,
	);
	const empty = $derived(
		tab === 'staged'
			? drafts.loaded && !drafts.error && lanes.length === 0 && failedLanes.length === 0
			: tab === 'review'
				? proposedLane.length === 0 && pushed === 0
				: platformHidden || (!scopeProject && repoProjects.length === 0),
	);
	const tabHref = (id: string) => `${page.url.pathname}?tab=${id}`;
</script>

<svelte:head><title>Changes - dotvirt</title></svelte:head>

{#snippet refusedLine()}
	{#if refused.length === 1}
		{@const r = refused[0]}
		The cluster refused {r.prNumber ? `PR #${r.prNumber}` : 'the last merge'}{scopeProject
			? ''
			: ` in ${r.project}`}.
		<a href={historyHref(r.project)} class="text-accent-ink underline underline-offset-2"
			>Open history</a
		>
	{:else}
		The cluster refused merges in {plural(refused.length, 'project')}:
		{#each refused as r, i (r.project)}{i ? ', ' : ''}<a
				href={historyHref(r.project)}
				class="text-accent-ink underline underline-offset-2">{r.project}</a
			>{/each}
	{/if}
{/snippet}

{#snippet wholeRepo(project: string, why: string)}
	<div class="flex max-w-2xl flex-col gap-2">
		{#if why}<p class="font-medium text-ink">{why}</p>{/if}
		<p class="text-ink-soft">
			A past change's review and its Undo read everything in {project}'s repo.
		</p>
		<p class="text-ink-soft">A VM restores its own earlier versions from its page.</p>
	</div>
{/snippet}

{#snippet nextLinks()}
	{#if tab === 'staged' && stagedTotal === 0 && proposedLane.length === 0 && inventory.vmCount > 0}
		<a href="/compute" class="text-accent-ink hover:underline">Change a VM in Compute</a>
	{/if}
	{#if tab !== 'staged' && stagedTotal > 0}
		<a href={tabHref('staged')} class="text-accent-ink hover:underline"
			>{plural(stagedTotal, 'staged change')} to propose</a
		>
	{/if}
	{#if tab !== 'review' && proposedLane.length > 0}
		<a href={tabHref('review')} class="text-accent-ink hover:underline"
			>{plural(proposedLane.length, 'pull request')} in review</a
		>
	{/if}
	{#if tab !== 'history' && repoProjects.length > 0}
		<a href={tabHref('history')} class="text-accent-ink hover:underline">Past changes</a>
	{/if}
{/snippet}

<div class="flex min-h-0 flex-1 flex-col bg-canvas">
	<div class="flex flex-col gap-2 px-6 pt-5">
		<Breadcrumb {trail} />
		<PageHeader {title} {status} {context} />
		<TabBar {tabs} active={tab} href={tabHref} />
	</div>

	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 pt-4 pb-7"
		role="region"
		aria-label="Changes, {tabLabel}"
		tabindex="0"
	>
		{#if scopeProject && !scopeTracked}
			<div class="overflow-hidden rounded-lg empty:hidden">
				<RepoBanner project={scopeProject} />
			</div>
		{/if}

		{#if empty}
			<Card title={tabLabel}>
				<div class="flex max-w-2xl flex-col gap-2">
					{#if tab === 'staged'}
						{#if proposedLane.length === 0}
							<p class="font-medium text-ink">
								Nothing in flight{scopeProject ? ` for ${scopeNS || scopeProject}` : ''}.
							</p>
							<p class="text-ink-soft">
								Every configuration change is staged here first, proposed as a pull request, and
								applied by ArgoCD after the merge. The audit trail is the project's git history.
							</p>
						{:else}
							<p class="font-medium text-ink">
								Nothing staged{scopeProject ? ` in ${scopeProject}` : ''}.
							</p>
							<p class="text-ink-soft">
								Edits, creates and deletes land here before becoming a pull request.
							</p>
						{/if}
					{:else if tab === 'review'}
						<p class="font-medium text-ink">
							No open pull requests{scopeProject ? ` in ${scopeProject}` : ''}.
						</p>
						<p class="text-ink-soft">
							A proposed draft waits here for approval and merge in the forge.
						</p>
					{:else if platformHidden}
						<p class="font-medium text-ink">You cannot read the platform's history.</p>
						<p class="text-ink-soft">A platform administrator can.</p>
						<p class="text-ink-soft">Your own pull requests there stay under In review.</p>
					{:else}
						<p class="font-medium text-ink">No repo-backed projects.</p>
						<p class="text-ink-soft">
							History lists the merged changes of every project with a repository.
						</p>
					{/if}
					<div class="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">{@render nextLinks()}</div>
				</div>
			</Card>
		{:else}
			<div class="flex flex-wrap items-start gap-4">
				<ChangesLanes
					{tab}
					{scopeProject}
					{lanes}
					{proposedLane}
					{visibleProposals}
					{mineOnly}
					{results}
					{history}
					{repoProjects}
					{scopeNS}
					{repoRead}
					{active}
					onselect={select}
					ontogglehistory={toggleHistory}
					onretryhistory={loadHistory}
					ondismiss={(project) => delete results[project]}
				/>

				<div class="flex min-w-0 flex-[2_1_520px] flex-col gap-4">
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
						{@const pr = selected.proposal}
						{@const reads = pr.mine ? null : repoRead(pr.project)}
						{#if !reads || reads.ok}
							<ReviewProposal proposal={pr} />
						{:else}
							<Card title="Review">
								{#if reads.why === CHECKING}
									<Skeleton rows={2} class="h-6" />
								{:else}
									<div class="flex max-w-2xl flex-col gap-2">
										<p class="font-medium text-ink">{reads.why}</p>
										<p class="text-ink-soft">
											Another's pull request reads everything in {pr.project}'s repo.
										</p>
										<a
											href={pr.prURL}
											target="_blank"
											rel="noopener"
											class="text-[13px] text-accent-ink hover:underline"
											>Open PR #{pr.prNumber} in the forge</a
										>
									</div>
								{/if}
							</Card>
						{/if}
					{:else if selected?.kind === 'commit'}
						{@const { project, hash } = selected}
						{@const reads = repoRead(project)}
						{#if reads.ok}
							<ReviewCommit
								{project}
								{hash}
								commit={selected.commit}
								done={reverts[commitKey(project, hash)]}
								onundo={() => (confirmUndo = { project, hash })}
							/>
						{:else}
							<Card title="Review">
								{#if reads.why === CHECKING}
									<Skeleton rows={2} class="h-6" />
								{:else}
									{@render wholeRepo(project, reads.why)}
								{/if}
							</Card>
						{/if}
					{:else}
						<Card title="Review">
							<div class="flex flex-col gap-2">
								{#if tab === 'history' && scopeRefused}
									<!-- Without a namespace, the lane already says why. -->
									{@render wholeRepo(scopeProject, scopeNS ? scopeReads!.why : '')}
								{:else if tab === 'history'}
									<p class="text-ink-soft">
										{scopeProject ? 'Pick a change' : 'Open a project and pick a change'} to review it
										here: what it did, and Undo, which opens a pull request that reverts it.
									</p>
								{:else if tab === 'review'}
									<p class="text-ink-soft">Pick a pull request to review its changes here.</p>
									{#if forgeOnly}
										<p class="text-ink-soft">
											Others' pull requests in a repo you cannot read all of open in the forge.
										</p>
									{/if}
								{:else}
									<p class="text-ink-soft">
										Pick a staged change to review it here and propose its project's draft.
									</p>
								{/if}
								<div class="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">{@render nextLinks()}</div>
							</div>
						</Card>
					{/if}
				</div>
			</div>
		{/if}
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
