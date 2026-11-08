<script lang="ts">
	import { untrack } from 'svelte';
	import {
		ChevronDown,
		ChevronRight,
		GitPullRequest,
		Pencil,
		Plus,
		Search,
		Trash2,
		TriangleAlert,
	} from '@lucide/svelte';
	import type { Commit, DraftItem, DraftView, Proposal, ProposeResult } from '#lib/api.js';
	import { plural, shortDate } from '#lib/format.js';
	import { changesHref, type ChangesTab } from '#lib/nav.js';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, pageOf, pageSlice } from '#lib/paging.js';
	import { leafWindow, matches } from '#lib/rail.js';
	import {
		approvalLine,
		checksPill,
		itemKey,
		prKey,
		sameReview,
		type ReviewSel,
	} from '#lib/review.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import DiscardDraft from '#lib/components/DiscardDraft.svelte';
	import Note from '#lib/components/Note.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// The list card of the Changes tab in view: staged drafts per project,
	// every open PR of the caller's projects, or history per repo-backed
	// project. Selection and history state belong to the workspace: the URL
	// mirrors the one, the review card resolves against the other. Lists that
	// grow with the fleet page; a short one shows whole.
	export type HistoryLane = { open: boolean; busy: boolean; error: string; commits: Commit[] };

	let {
		tab = 'staged',
		scopeProject,
		lanes,
		proposedLane,
		visibleProposals,
		mineOnly,
		results,
		history,
		repoProjects,
		active,
		onselect,
		ontogglehistory,
		onretryhistory,
		ondismiss,
	}: {
		tab?: ChangesTab;
		scopeProject: string;
		lanes: { project: string; draft: DraftView }[];
		proposedLane: Proposal[];
		visibleProposals: Proposal[];
		mineOnly: { value: boolean };
		// Propose outcomes the stream has not carried yet, by project.
		results: Record<string, ProposeResult>;
		history: Record<string, HistoryLane>;
		repoProjects: string[];
		active: ReviewSel | null;
		onselect: (s: ReviewSel) => void;
		ontogglehistory: (project: string) => void;
		onretryhistory: (project: string) => void;
		ondismiss: (project: string) => void;
	} = $props();

	// Lanes asking to discard their draft, each on its own.
	let discarding = $state<Record<string, boolean>>({});

	const on = (s: ReviewSel) => sameReview(active, s);
	const failedLanes = $derived(
		drafts.failed.filter((f) => !scopeProject || f.project === scopeProject),
	);
	const pushed = $derived(Object.entries(results).filter(([, r]) => !r.prURL));

	// A list pages only past the smallest page size: below it there is nothing
	// to page, and the card header already counts it.
	const paged = (total: number) => total > PAGE_SIZES[0];
	let size = $state(DEFAULT_PAGE_SIZE);

	// --- Staged: the drafts' items, paged as one list under their projects ---
	const rowKey = (project: string, it: DraftItem) => `${project}|${itemKey(it)}`;
	const stagedRows = $derived(
		lanes.flatMap((l) => l.draft.items.map((it) => rowKey(l.project, it))),
	);
	let stagedPage = $state(1);
	const stagedSlice = $derived(pageSlice(stagedRows.length, stagedPage, size));
	const stagedOnPage = $derived(
		new Set(
			paged(stagedRows.length) ? stagedRows.slice(stagedSlice.start, stagedSlice.end) : stagedRows,
		),
	);
	// Warning-only lanes have no rows: they lead the first page.
	const lanesOnPage = $derived(
		lanes.filter((l) =>
			l.draft.items.length === 0
				? stagedSlice.page === 1
				: l.draft.items.some((it) => stagedOnPage.has(rowKey(l.project, it))),
		),
	);
	const stagedProjects = $derived(lanes.filter((l) => l.draft.count > 0).length);

	// --- In review ---
	let reviewPage = $state(1);
	const reviewSlice = $derived(pageSlice(visibleProposals.length, reviewPage, size));
	const reviewRows = $derived(
		paged(visibleProposals.length)
			? visibleProposals.slice(reviewSlice.start, reviewSlice.end)
			: visibleProposals,
	);

	// --- History at the root: projects, paged and filtered; one opens inline ---
	let query = $state('');
	let historyPage = $state(1);
	const historyProjects = $derived(repoProjects.filter((p) => matches(query, p)));
	const historySlice = $derived(pageSlice(historyProjects.length, historyPage, size));
	const historyRows = $derived(
		paged(historyProjects.length)
			? historyProjects.slice(historySlice.start, historySlice.end)
			: historyProjects,
	);
	// An open project lists its newest commits; the rest are one click away on
	// its own History tab. The selected commit always shows.
	const INLINE_COMMITS = 5;

	// A selection made elsewhere (a deep link, a propose) brings its row into
	// view. Keyed on the selection's identity, so paging by hand stays put.
	const activeKey = $derived(
		!active
			? ''
			: active.kind === 'item'
				? `item|${active.project}|${active.key}`
				: active.kind === 'proposal'
					? `pr|${prKey(active.project, active.prNumber)}`
					: `commit|${active.project}`,
	);
	$effect(() => {
		if (!activeKey) return;
		untrack(() => {
			if (active?.kind === 'item') {
				const i = stagedRows.indexOf(`${active.project}|${active.key}`);
				if (i >= 0) stagedPage = pageOf(i, size);
			} else if (active?.kind === 'proposal') {
				const i = visibleProposals.findIndex(
					(p) => p.project === active.project && p.prNumber === active.prNumber,
				);
				if (i >= 0) reviewPage = pageOf(i, size);
			} else if (active?.kind === 'commit' && !scopeProject) {
				const i = historyProjects.indexOf(active.project);
				if (i >= 0) historyPage = pageOf(i, size);
			}
		});
	});

	// List rows: a hairline above each (the body drops the first), the
	// selected one tinted with the accent edge the tables use.
	const ROW = 'flex w-full items-center gap-2 border-t border-line-soft text-left';
	const tint = (selected: boolean) =>
		selected ? 'bg-select shadow-[inset_3px_0_0_var(--color-accent)]' : 'hover:bg-select-soft';
	const INDENT = 'pr-card pl-[calc(var(--card-x)+22px)]';
	const BODY = '[&>*:first-child]:border-t-0';
</script>

{#snippet retryDrafts()}
	<Button variant="link" size="sm" disabled={drafts.refreshing} onclick={() => drafts.refresh()}
		>Retry</Button
	>
{/snippet}

{#snippet pager(total: number, page: number, setPage: (n: number) => void)}
	{#if paged(total)}
		<Pager
			class="border-t border-line px-card py-3"
			{total}
			bind:page={() => page, setPage}
			bind:size
		/>
	{/if}
{/snippet}

{#snippet commitRow(project: string, c: Commit, indent: boolean)}
	{@const sel = { kind: 'commit', project, hash: c.hash } as const}
	<button
		onclick={() => onselect(sel)}
		class="{ROW} min-h-10 py-2 text-[13px] {indent ? INDENT : 'px-card'} {tint(on(sel))}"
	>
		<code class="shrink-0 font-mono text-xs text-ink-faint">{c.shortHash}</code>
		<span class="min-w-0 truncate text-ink" title={c.message}>{c.title}</span>
		{#if c.prNumber}<span class="shrink-0 text-ink-faint">#{c.prNumber}</span>{/if}
		<span class="ml-auto shrink-0 text-xs text-ink-faint">{shortDate(c.when)}</span>
	</button>
{/snippet}

{#snippet laneBody(project: string, lane: HistoryLane | undefined, inline: boolean)}
	{#if !lane || (lane.busy && !lane.commits.length)}
		<div class="border-t border-line-soft px-card py-3"><Skeleton rows={2} class="h-6" /></div>
	{:else if lane.error}
		<div class="border-t border-line-soft px-card py-2.5">
			<Note tone="danger" class="flex items-center gap-2">
				<span class="min-w-0 flex-1">Could not read the history: {lane.error}</span>
				<Button variant="link" size="sm" onclick={() => onretryhistory(project)}>Retry</Button>
			</Note>
		</div>
	{:else if inline}
		{@const shown = leafWindow(
			lane.commits,
			INLINE_COMMITS,
			(c) => active?.kind === 'commit' && active.project === project && active.hash === c.hash,
		)}
		{#each shown.shown as c (c.hash)}
			{@render commitRow(project, c, true)}
		{:else}
			<p class="border-t border-line-soft py-2 text-[13px] text-ink-faint {INDENT}">
				No commits yet.
			</p>
		{/each}
		{#if shown.more > 0}
			<a
				href="{changesHref(project)}?tab=history"
				class="block border-t border-line-soft py-2 text-[13px] text-accent-ink hover:underline {INDENT}"
				>All {lane.commits.length} recent changes in {project}</a
			>
		{/if}
	{:else}
		{#each lane.commits as c (c.hash)}
			{@render commitRow(project, c, false)}
		{:else}
			<p class="border-t border-line-soft px-card py-3 text-[13px] text-ink-faint">
				No commits yet.
			</p>
		{/each}
	{/if}
{/snippet}

{#if tab === 'staged'}
	<Card
		title="Staged"
		meta={stagedRows.length
			? `${plural(stagedRows.length, 'change')}${stagedProjects > 1 ? ` in ${plural(stagedProjects, 'project')}` : ''}`
			: undefined}
		flush
		class="flex-[1_1_360px]"
	>
		<div class={BODY}>
			{#if !drafts.loaded}
				<div class="px-card py-3"><Skeleton rows={3} class="h-7" /></div>
			{/if}
			{#each failedLanes as f (f.project)}
				<div class="flex flex-col gap-2 border-t border-line-soft px-card py-3">
					<span class="font-medium text-ink">{f.project}</span>
					<Note tone="danger" class="flex items-center gap-2">
						<span class="min-w-0 flex-1">Could not read the draft: {f.error}</span>
						{@render retryDrafts()}
					</Note>
				</div>
			{/each}
			{#each lanesOnPage as { project, draft } (project)}
				<div class="flex flex-col gap-2 border-t border-line-soft px-card py-2.5">
					<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
						<span class="font-medium text-ink">{project}</span>
						<span class="text-[13px] text-ink-faint">{plural(draft.count, 'change')}</span>
						{#if draft.count > 0 && !discarding[project]}
							<Button
								variant="danger-secondary"
								size="sm"
								class="ml-auto"
								onclick={() => (discarding[project] = true)}>Discard all</Button
							>
						{/if}
					</div>
					{#if discarding[project]}
						<div class="flex flex-col gap-1.5 rounded-md bg-inset px-3 py-2.5 text-[13px]">
							<DiscardDraft
								{project}
								count={draft.count}
								onclose={() => delete discarding[project]}
							/>
							<p class="text-xs leading-4 text-ink-faint">
								Drops every staged change in {project}. Nothing reaches git.
							</p>
						</div>
					{/if}
					{#if draft.warning}
						<Note tone="warn" class="flex items-start gap-2">
							<TriangleAlert size={14} class="mt-0.5 shrink-0" />
							<span>{draft.warning}</span>
						</Note>
					{/if}
				</div>
				{#each draft.items.filter( (it) => stagedOnPage.has(rowKey(project, it)) ) as it (itemKey(it))}
					{@const sel = { kind: 'item', project, key: itemKey(it) } as const}
					<button
						data-project={project}
						onclick={() => onselect(sel)}
						class="{ROW} h-10 px-card {tint(on(sel))}"
					>
						{#if it.kind === 'delete'}<Trash2 size={14} class="shrink-0 text-danger-ink" />
						{:else if it.kind === 'create'}<Plus size={14} class="shrink-0 text-ok-ink" />
						{:else}<Pencil size={14} class="shrink-0 text-accent-ink" />{/if}
						<span class="shrink-0 font-mono text-[13px] font-medium text-ink">{it.name}</span>
						<span class="min-w-0 truncate text-[13px] text-ink-muted">
							{it.kind === 'delete'
								? 'Delete'
								: it.kind === 'create'
									? 'Create'
									: it.changes
											.slice(0, 3)
											.map((c) => c.field)
											.join(', ')}
						</span>
					</button>
				{/each}
			{/each}
		</div>
		{@render pager(stagedRows.length, stagedSlice.page, (n) => (stagedPage = n))}
	</Card>
{:else if tab === 'review'}
	<Card
		title="Open pull requests"
		meta={proposedLane.length
			? mineOnly.value
				? `${visibleProposals.length} of ${proposedLane.length}`
				: String(proposedLane.length)
			: undefined}
		flush
		class="flex-[1_1_360px]"
	>
		{#snippet actions()}
			{#if proposedLane.length > 0}
				<span
					class="inline-flex overflow-hidden rounded-md border border-line"
					role="group"
					aria-label="Whose pull requests"
				>
					{#each [false, true] as mine (mine)}
						<button
							aria-pressed={mineOnly.value === mine}
							onclick={() => (mineOnly.value = mine)}
							class="h-6 px-2.5 text-[13px] not-first:border-l not-first:border-line {mineOnly.value ===
							mine
								? 'bg-select font-medium text-accent-ink'
								: 'bg-panel text-ink-soft hover:text-ink'}">{mine ? 'Mine' : 'All'}</button
						>
					{/each}
				</span>
			{/if}
		{/snippet}
		<div class={BODY}>
			{#if visibleProposals.length === 0 && proposedLane.length > 0}
				<p class="border-t border-line-soft px-card py-3 text-[13px] text-ink-faint">
					None of the open pull requests are yours.
					<button
						class="text-accent-ink underline underline-offset-2"
						onclick={() => (mineOnly.value = false)}>Show all {proposedLane.length}</button
					>
				</p>
			{/if}
			{#each reviewRows as p (prKey(p.project, p.prNumber))}
				{@const sel = { kind: 'proposal', project: p.project, prNumber: p.prNumber } as const}
				{@const appr = approvalLine(p)}
				{@const chk = checksPill(p)}
				<button
					onclick={() => onselect(sel)}
					class="flex w-full flex-col gap-1 border-t border-line-soft px-card py-2.5 text-left {tint(
						on(sel),
					)}"
				>
					<span class="flex items-center gap-2">
						<GitPullRequest size={14} class="shrink-0 text-accent-ink" />
						<span class="text-[13px] font-medium text-ink">PR #{p.prNumber}</span>
						<span class="text-[13px] text-ink-muted">{p.project}</span>
						{#if p.by && !p.mine}<span class="text-[13px] text-ink-faint">by {p.by}</span>{/if}
						{#if chk}<span class="ml-auto"><StatusPill tone={chk.tone} label={chk.text} /></span
							>{/if}
					</span>
					{#if p.title || appr}
						<span class="flex items-center gap-2 pl-[22px]">
							<span class="min-w-0 flex-1 truncate text-[13px] text-ink-soft">{p.title}</span>
							{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
						</span>
					{/if}
				</button>
			{/each}
			<!-- Push-only propose outcomes (no PR yet): keep the compare link visible. -->
			{#each pushed as [project, r] (project)}
				<div class="border-t border-line-soft px-card py-2.5 text-[13px] text-ink-soft">
					<span class="font-medium text-ink">{project}</span>: branch
					<code class="font-mono text-xs">{r.branch}</code> pushed.
					{#if r.compareURL}
						<a
							href={r.compareURL}
							target="_blank"
							rel="noopener"
							class="text-accent-ink underline underline-offset-2">Open the pull request</a
						>
					{:else}
						No forge is configured to open it.
					{/if}
					<button onclick={() => ondismiss(project)} class="ml-1 text-ink-faint hover:text-ink-soft"
						>Dismiss</button
					>
				</div>
			{/each}
		</div>
		{@render pager(visibleProposals.length, reviewSlice.page, (n) => (reviewPage = n))}
	</Card>
{:else if scopeProject}
	<Card
		title="History"
		meta={history[scopeProject]?.commits.length
			? plural(history[scopeProject].commits.length, 'change')
			: undefined}
		flush
		class="flex-[1_1_360px]"
	>
		{#if repoProjects.includes(scopeProject)}
			<div class={BODY}>
				{@render laneBody(scopeProject, history[scopeProject], false)}
			</div>
		{:else}
			<p class="px-card py-3 text-[13px] text-ink-faint">This project has no repository yet.</p>
		{/if}
	</Card>
{:else}
	<Card
		title="History"
		meta={plural(repoProjects.length, 'project')}
		flush
		class="flex-[1_1_360px]"
	>
		{#snippet actions()}
			{#if repoProjects.length > 10}
				<label class="relative block w-44">
					<Search size={14} class="pointer-events-none absolute top-[7px] left-2 text-ink-faint" />
					<span class="sr-only">Find a project</span>
					<input
						type="search"
						bind:value={query}
						oninput={() => (historyPage = 1)}
						placeholder="Find a project"
						class="h-7 w-full rounded-md border border-line-strong bg-panel pr-2 pl-7 text-[13px] text-ink placeholder:text-ink-faint"
					/>
				</label>
			{/if}
		{/snippet}
		<div class={BODY}>
			{#each historyRows as project (project)}
				{@const lane = history[project]}
				<button
					onclick={() => ontogglehistory(project)}
					aria-expanded={!!lane?.open}
					class="{ROW} h-10 px-card text-[13px] text-ink hover:bg-select-soft"
				>
					{#if lane?.open}<ChevronDown size={14} class="shrink-0 text-ink-muted" />
					{:else}<ChevronRight size={14} class="shrink-0 text-ink-muted" />{/if}
					{project}
				</button>
				{#if lane?.open}
					{@render laneBody(project, lane, true)}
				{/if}
			{:else}
				<p class="border-t border-line-soft px-card py-3 text-[13px] text-ink-faint">
					{repoProjects.length ? 'No project matches.' : 'No repo-backed projects.'}
				</p>
			{/each}
		</div>
		{@render pager(historyProjects.length, historySlice.page, (n) => (historyPage = n))}
	</Card>
{/if}
