<script lang="ts">
	import {
		ChevronDown,
		ChevronRight,
		Folder,
		GitPullRequest,
		History,
		Pencil,
		Plus,
		Trash2,
		TriangleAlert,
	} from 'lucide-svelte';
	import { api, type Commit, type DraftView, type Proposal, type ProposeResult } from '$lib/api';
	import { shortDate } from '$lib/format';
	import { action } from '$lib/resource.svelte';
	import {
		approvalLine,
		checksPill,
		itemKey,
		prKey,
		sameReview,
		type ReviewSel,
	} from '$lib/review';
	import { drafts } from '$lib/state/drafts.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import Note from '$lib/components/Note.svelte';
	import Skeleton from '$lib/components/Skeleton.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';

	// The left column: the lifecycle of the scope's changes. Staged drafts per
	// project, every open PR of the caller's projects, history per repo-backed
	// project. Selection and history state belong to the workspace: the URL
	// mirrors the one, the review pane resolves against the other.
	export type HistoryLane = { open: boolean; busy: boolean; error: string; commits: Commit[] };

	let {
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
		ondismiss,
	}: {
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
		ondismiss: (project: string) => void;
	} = $props();

	const discardOp = action();
	async function discardAll(project: string) {
		if (discardOp.busy) return;
		if (await discardOp.run(() => api.discardDraft(project))) drafts.refresh();
	}

	const on = (s: ReviewSel) => sameReview(active, s);
</script>

<div class="flex w-96 shrink-0 flex-col overflow-y-auto border-r border-line">
	<div class="px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase">
		Staged
	</div>
	<ErrorNote error={discardOp.error} class="mx-3 mb-1" />
	{#if !drafts.loaded}
		<div class="p-3"><Skeleton rows={3} class="h-7" /></div>
	{:else if lanes.length === 0}
		<p class="px-3 py-2 text-xs text-ink-faint">
			Nothing staged{scopeProject ? ` in ${scopeProject}` : ''}. Edits, creates and deletes land
			here before becoming a pull request.
		</p>
	{/if}
	{#each lanes as { project, draft } (project)}
		<div class="flex items-center gap-2 px-3 py-1.5">
			<Folder size={14} class="text-ink-faint" />
			<span class="font-medium text-ink">{project}</span>
			<span class="text-xs text-ink-faint">{draft.count} change{draft.count > 1 ? 's' : ''}</span>
			<button
				onclick={() => discardAll(project)}
				disabled={discardOp.busy}
				class="ml-auto text-[11px] text-ink-muted hover:text-danger-ink disabled:text-ink-faint"
				>{discardOp.busy ? 'discarding…' : 'discard all'}</button
			>
		</div>
		{#if draft.warning}
			<Note tone="warn" class="mx-3 mb-1 flex items-start gap-2">
				<TriangleAlert size={14} class="mt-0.5 shrink-0" />
				<span>{draft.warning}</span>
			</Note>
		{/if}
		{#each draft.items as it (itemKey(it))}
			{@const sel = { kind: 'item', project, key: itemKey(it) } as const}
			<button
				data-project={project}
				onclick={() => onselect(sel)}
				class="flex w-full items-center gap-2 py-1.5 pr-3 pl-7 text-left hover:bg-select-soft {on(
					sel,
				)
					? 'bg-select hover:bg-select'
					: ''}"
			>
				{#if it.kind === 'delete'}<Trash2 size={13} class="shrink-0 text-danger-ink" />
				{:else if it.kind === 'create'}<Plus size={13} class="shrink-0 text-ok-ink" />
				{:else}<Pencil size={13} class="shrink-0 text-accent-ink" />{/if}
				<span class="truncate text-[13px] font-medium text-ink">{it.name}</span>
				<span class="min-w-0 truncate text-xs text-ink-muted">
					{it.kind === 'delete'
						? 'Delete'
						: it.changes
								.slice(0, 3)
								.map((c) => c.field)
								.join(', ')}
				</span>
			</button>
		{/each}
	{/each}

	<div
		class="mt-3 flex items-center border-t border-line px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase"
	>
		Proposed
		{#if proposedLane.length > 0}
			<span class="ml-auto flex gap-2 normal-case">
				<button
					onclick={() => (mineOnly.value = false)}
					class="hover:text-ink {mineOnly.value ? '' : 'text-ink'}">All</button
				>
				<button
					onclick={() => (mineOnly.value = true)}
					class="hover:text-ink {mineOnly.value ? 'text-ink' : ''}">Mine</button
				>
			</span>
		{/if}
	</div>
	{#if proposedLane.length === 0}
		<p class="px-3 py-2 text-xs text-ink-faint">No open pull requests.</p>
	{:else if visibleProposals.length === 0}
		<p class="px-3 py-2 text-xs text-ink-faint">None of the open pull requests are yours.</p>
	{/if}
	{#each visibleProposals as p (prKey(p.project, p.prNumber))}
		{@const sel = { kind: 'proposal', project: p.project, prNumber: p.prNumber } as const}
		{@const appr = approvalLine(p)}
		{@const chk = checksPill(p)}
		<button
			onclick={() => onselect(sel)}
			class="w-full px-3 py-2 text-left hover:bg-select-soft {on(sel)
				? 'bg-select hover:bg-select'
				: ''}"
		>
			<div class="flex items-center gap-2">
				<GitPullRequest size={13} class="shrink-0 text-accent-ink" />
				<span class="text-[13px] font-medium text-ink">PR #{p.prNumber}</span>
				<span class="text-xs text-ink-muted">{p.project}</span>
				{#if p.by && !p.mine}<span class="text-xs text-ink-faint">by {p.by}</span>{/if}
				{#if chk}<StatusPill tone={chk.tone} label={chk.text} />{/if}
			</div>
			{#if p.title || appr}
				<div class="mt-1 flex items-center gap-2 pl-6">
					{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
					<span class="min-w-0 truncate text-xs text-ink-soft">{p.title}</span>
				</div>
			{/if}
		</button>
	{/each}
	<!-- Push-only propose outcomes (no PR yet): keep the compare link visible. -->
	{#each Object.entries(results).filter(([, r]) => !r.prURL) as [project, r] (project)}
		<div class="mx-3 my-1 rounded border border-line px-2 py-1.5 text-xs text-ink-soft">
			<span class="font-medium">{project}</span>: branch <code>{r.branch}</code> pushed —
			{#if r.compareURL}
				<a href={r.compareURL} target="_blank" rel="noopener" class="underline">open PR</a>
			{:else}
				no forge configured.
			{/if}
			<button onclick={() => ondismiss(project)} class="ml-1 text-ink-faint hover:text-ink-soft"
				>dismiss</button
			>
		</div>
	{/each}

	<div
		class="mt-3 border-t border-line px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase"
	>
		History
	</div>
	<div class="pb-3">
		{#each repoProjects as project (project)}
			{@const lane = history[project]}
			<button
				onclick={() => ontogglehistory(project)}
				class="flex w-full items-center gap-2 px-3 py-1 text-left text-[13px] text-ink-soft hover:bg-select-soft"
			>
				{#if lane?.open}<ChevronDown size={12} class="text-ink-faint" />
				{:else}<ChevronRight size={12} class="text-ink-faint" />{/if}
				<History size={12} class="text-ink-faint" />
				{project}
			</button>
			{#if lane?.open}
				{#if lane.busy}
					<p class="py-1 pl-10 text-xs text-ink-faint">loading…</p>
				{:else if lane.error}
					<p class="py-1 pl-10 text-xs text-danger-ink">{lane.error}</p>
				{:else}
					{#each lane.commits as c (c.hash)}
						{@const sel = { kind: 'commit', project, hash: c.hash } as const}
						<button
							onclick={() => onselect(sel)}
							class="flex w-full items-baseline gap-2 py-1 pr-3 pl-10 text-left text-xs hover:bg-select-soft {on(
								sel,
							)
								? 'bg-select hover:bg-select'
								: ''}"
						>
							<code class="shrink-0 text-ink-faint">{c.shortHash}</code>
							<span class="min-w-0 truncate text-ink-soft" title={c.message}>{c.title}</span>
							{#if c.prNumber}<span class="shrink-0 text-ink-faint">#{c.prNumber}</span>{/if}
							<span class="ml-auto shrink-0 text-ink-faint">{shortDate(c.when)}</span>
						</button>
					{:else}
						<p class="py-1 pl-10 text-xs text-ink-faint">no commits</p>
					{/each}
				{/if}
			{/if}
		{:else}
			<p class="px-3 py-1 text-xs text-ink-faint">
				{scopeProject ? 'This project has no repository yet.' : 'No repo-backed projects.'}
			</p>
		{/each}
	</div>
</div>
