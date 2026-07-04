<script lang="ts">
	import { untrack } from 'svelte';
	import { ChevronDown, ChevronRight, Folder, History } from 'lucide-svelte';
	import { api, type Commit, type DraftView, type Proposal, type ProposeResult } from '$lib/api';
	import ChangesLane from './ChangesLane.svelte';
	import Drawer from './Drawer.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';

	let {
		drafts,
		proposals,
		projects,
		loaded = true,
		refreshing = false,
		onclose,
		onchanged,
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		projects: string[]; // repo-backed project names, for the History section
		// Draft-summary fetch state: `loaded` gates the empty state (never claim
		// "no changes" before a summary has landed), `refreshing` shows in the title.
		loaded?: boolean;
		refreshing?: boolean;
		onclose: () => void;
		onchanged: () => void;
	} = $props();

	// Propose results outlive their lane (a proposed lane empties and unmounts),
	// so they're held here until the persistent PR banner carries that exact PR.
	// The lane's typed title rides along for the synthesized banner.
	let result = $state<Record<string, ProposeResult & { title?: string }>>({});

	// A propose that landed a PR renders through the SAME banner as a live PR
	// lane, so when the streamed proposal arrives it takes over invisibly —
	// never a differently-styled box that swaps to "the definitive one".
	const banners = $derived.by(() => {
		const out: { project: string; prNumber: number; prURL: string; title?: string }[] = [
			...proposals,
		];
		const seen = new Set(out.map((p) => `${p.project}#${p.prNumber}`));
		for (const [project, r] of Object.entries(result)) {
			if (!r.prURL || !r.prNumber) continue; // push-only outcomes keep their own note below
			if (seen.has(`${project}#${r.prNumber}`)) continue;
			out.push({ project, prNumber: r.prNumber, prURL: r.prURL, title: r.title });
		}
		return out.sort((a, b) => a.project.localeCompare(b.project));
	});

	// Once the live stream carries a proposed PR, its transient result is spent —
	// kept around it would resurface as a ghost banner after the PR merges.
	$effect(() => {
		for (const p of proposals) {
			if (untrack(() => result[p.project])?.prNumber === p.prNumber) delete result[p.project];
		}
	});

	// Commit history: per-project, re-fetched on every expand (a merge or revert
	// changes it). Revert state is keyed by commit hash — armed (awaiting a
	// confirm click), busy (in flight), and result.
	let showHistory = $state(false);
	let historyOpen = $state<Record<string, boolean>>({});
	let history = $state<Record<string, Commit[]>>({});
	let historyBusy = $state<Record<string, boolean>>({});
	let historyError = $state<Record<string, string>>({});
	let revertArmed = $state<string | null>(null);
	let revertBusy = $state<string | null>(null);
	let revertResult = $state<Record<string, ProposeResult>>({});

	const total = $derived(drafts.reduce((n, d) => n + d.draft.count, 0));

	async function toggleHistory(project: string) {
		historyOpen[project] = !historyOpen[project];
		if (historyOpen[project]) await loadHistory(project);
	}

	async function loadHistory(project: string) {
		historyBusy[project] = true;
		historyError[project] = '';
		try {
			history[project] = await api.history(project);
		} catch (e) {
			historyError[project] = String(e);
		} finally {
			historyBusy[project] = false;
		}
	}

	// Revert is two-click: the first arms the commit, the second fires it. It opens a
	// forward-commit PR (restoring the pre-commit state), never a history rewrite, so
	// the result surfaces inline as a PR link the user merges to land the revert.
	async function revert(project: string, c: Commit) {
		if (revertArmed !== c.hash) {
			revertArmed = c.hash;
			return;
		}
		revertArmed = null;
		revertBusy = c.hash;
		historyError[project] = '';
		try {
			revertResult[c.hash] = await api.revert(project, c.hash);
			onchanged();
		} catch (e) {
			historyError[project] = String(e);
		} finally {
			revertBusy = null;
		}
	}

	// Commits dotvirt wrote before mid-2026 carried the Unix epoch as their date (a
	// since-fixed byte-stable-re-propose hack); floor guards those so they read "—"
	// rather than a misleading "56 years ago".
	const EPOCH_FLOOR = Date.UTC(2020, 0, 1);

	// fmtWhen renders a commit's author date as a compact absolute date.
	function fmtWhen(iso: string): string {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t) || t < EPOCH_FLOOR) return '—';
		return new Date(t).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
		});
	}
</script>

<!-- One rendering for every propose outcome: a PR, a pushed branch awaiting a
     PR, or a local-only branch. -->
{#snippet prNote(r: ProposeResult, label: string)}
	{#if r.prURL}
		{label}{r.existing ? ' (existing)' : ''}:
		<a href={r.prURL} target="_blank" rel="noopener" class="font-medium underline"
			>#{r.prNumber ?? r.prURL}</a
		>
	{:else if r.compareURL}
		Branch <code>{r.branch}</code> pushed —
		<a href={r.compareURL} target="_blank" rel="noopener" class="font-medium underline">open PR</a>
	{:else}
		Branch <code>{r.branch}</code> pushed{r.pushed ? '' : ' (local only)'}.
	{/if}
{/snippet}

<Drawer title="Changes" count={loaded ? total : undefined} busy={refreshing} {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		{#if !loaded}
			<!-- First open before the summary has landed: a skeleton, never a false
			     "no pending changes". The last good summary renders during refreshes. -->
			<div class="space-y-2 py-2">
				{#each Array(3) as _, i (i)}
					<div class="h-8 animate-pulse rounded bg-inset-strong"></div>
				{/each}
			</div>
		{/if}
		<!-- Open PRs: the live lanes off the inventory stream, plus any just-proposed
		     PR straight from its propose response (same markup — the stream takes
		     over invisibly once it carries the PR). -->
		{#each banners as p (p.project + '#' + p.prNumber)}
			<div class="mb-2 rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm">
				<div class="flex items-center gap-2">
					<span class="font-medium text-ink-soft">{p.project}</span>
					<span class="rounded bg-emerald-100 px-1.5 text-xs font-medium text-ok-ink">
						PR #{p.prNumber} open
					</span>
					<a
						href={p.prURL}
						target="_blank"
						rel="noopener"
						class="ml-auto min-w-0 truncate text-xs text-accent-ink underline">{p.title || p.prURL}</a
					>
				</div>
				<div class="mt-1.5">
					<GitOpsStepper stage="proposed" prNumber={p.prNumber} prUrl={p.prURL} />
				</div>
			</div>
		{/each}

		<!-- Push-only propose outcomes (no PR to banner): a branch was pushed, or
		     stayed local — surface the compare link so the user can open the PR. -->
		{#each Object.entries(result).filter(([, r]) => !r.prURL || !r.prNumber) as [project, r] (project)}
			<div class="mb-2 rounded border border-green-200 bg-green-50 p-3 text-sm">
				<div class="mb-1 flex items-center gap-1">
					<span class="font-medium text-ink-soft">{project}</span>
					<button
						onclick={() => delete result[project]}
						class="ml-auto text-xs text-ink-faint hover:text-ink-soft">dismiss</button
					>
				</div>
				<p class="text-ink-soft">{@render prNote(r, 'Pull request opened')}</p>
			</div>
		{/each}

		{#if loaded && total === 0 && banners.length > 0}
			<p class="py-4 text-center text-sm text-ink-faint">
				Nothing staged — the open pull requests above carry everything proposed.
			</p>
		{:else if loaded && total === 0}
			<!-- The one place the write model is explained: like vCenter's Recent
			     Tasks, except every config change is a reviewable PR before it applies. -->
			<div class="py-8 text-center">
				<p class="mb-4 text-sm text-ink-faint">No pending changes.</p>
				<ol class="mx-auto max-w-[20rem] space-y-2 text-left text-xs text-ink-soft">
					<li class="flex items-start gap-2">
						<span
							class="mt-px flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-select text-[10px] font-semibold text-accent-ink"
							>1</span
						>
						Edit or create a VM — the change is staged here, not applied.
					</li>
					<li class="flex items-start gap-2">
						<span
							class="mt-px flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-select text-[10px] font-semibold text-accent-ink"
							>2</span
						>
						Propose — dotvirt opens a pull request in the project's repo.
					</li>
					<li class="flex items-start gap-2">
						<span
							class="mt-px flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-select text-[10px] font-semibold text-accent-ink"
							>3</span
						>
						Merge it — ArgoCD applies the change to the cluster.
					</li>
				</ol>
				<p class="mx-auto mt-4 max-w-[20rem] text-xs text-ink-faint">
					Every configuration change is a reviewable pull request before it touches the cluster.
				</p>
			</div>
		{/if}

		{#each drafts as { project, draft } (project)}
			<ChangesLane
				{project}
				{draft}
				{onchanged}
				onproposed={(r, title) => (result[project] = { ...r, title })}
			/>
		{/each}

		<!-- Commit history per repo-backed project, lazy-fetched on expand. Any
		     non-merge commit can be reverted as a forward-commit PR (never a rewrite). -->
		{#if loaded && projects.length > 0}
			<section class="mt-4 border-t border-line pt-3">
				<button
					onclick={() => (showHistory = !showHistory)}
					class="flex w-full items-center gap-1.5 text-sm font-semibold text-ink-soft"
				>
					{#if showHistory}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
					<History size={14} class="text-ink-faint" /> History
				</button>

				{#if showHistory}
					<div class="mt-2 space-y-3">
						{#each projects as project (project)}
							<div>
								<button
									onclick={() => toggleHistory(project)}
									class="flex w-full items-center gap-2 text-xs font-medium text-ink-soft hover:text-ink"
								>
									{#if historyOpen[project]}<ChevronDown size={12} />{:else}<ChevronRight
											size={12}
										/>{/if}
									<Folder size={12} class="text-accent" />
									{project}
								</button>

								{#if historyOpen[project]}
									{#if historyBusy[project]}
										<p class="px-5 py-1.5 text-xs text-ink-faint">Loading…</p>
									{:else if historyError[project]}
										<p class="px-5 py-1.5 text-xs whitespace-pre-wrap text-danger">
											{historyError[project]}
										</p>
									{:else if (history[project] ?? []).length === 0}
										<p class="px-5 py-1.5 text-xs text-ink-faint">No commits.</p>
									{:else}
										<ul class="mt-1 ml-1.5 border-l border-line">
											{#each history[project] as c (c.hash)}
												<li class="group py-1 pl-3">
													<div class="flex items-start gap-2">
														<div class="min-w-0 flex-1">
															<p class="truncate text-xs text-ink-soft" title={c.message}>
																{c.message}
															</p>
															<p class="text-[10px] text-ink-faint">
																<code class="text-ink-muted">{c.shortHash}</code>
																· {c.author} ·
																<span title={c.when}>{fmtWhen(c.when)}</span>{#if c.merge}
																	·
																	<span class="text-ink-faint">merge</span>{/if}
															</p>
														</div>
														{#if !c.merge && !revertResult[c.hash]}
															<button
																onclick={() => revert(project, c)}
																disabled={revertBusy === c.hash}
																class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium {revertArmed ===
																	c.hash || revertBusy === c.hash
																	? 'bg-warn-soft text-warn-ink'
																	: 'text-warn-ink opacity-0 hover:bg-warn-soft/60 group-hover:opacity-100'}"
															>
																{revertBusy === c.hash
																	? 'Reverting…'
																	: revertArmed === c.hash
																		? 'Confirm revert'
																		: 'Revert'}
															</button>
														{/if}
													</div>
													{#if revertResult[c.hash]}
														<p class="mt-0.5 text-[10px] text-ok-ink">
															{@render prNote(revertResult[c.hash], 'Revert PR')}
														</p>
													{/if}
												</li>
											{/each}
										</ul>
									{/if}
								{/if}
							</div>
						{/each}
					</div>
				{/if}
			</section>
		{/if}
	</div>
</Drawer>
