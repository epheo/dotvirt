<script lang="ts">
	import { ChevronDown, ChevronRight, Folder, History, X } from 'lucide-svelte';
	import { api, type Commit, type DraftView, type Proposal, type ProposeResult } from '$lib/api';
	import ChangeList from './ChangeList.svelte';

	let {
		drafts,
		proposals,
		projects,
		onclose,
		onchanged
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		projects: string[]; // repo-backed project names, for the History section
		onclose: () => void;
		onchanged: () => void;
	} = $props();

	// Per-project PR form state, keyed by project name.
	let title = $state<Record<string, string>>({});
	let message = $state<Record<string, string>>({});
	let busy = $state<Record<string, boolean>>({});
	let error = $state<Record<string, string>>({});
	let result = $state<Record<string, ProposeResult>>({});
	let showYaml = $state<Record<string, boolean>>({});

	// Commit history: per-project, lazy-fetched on expand. Revert state is keyed by
	// commit hash — armed (awaiting a confirm click), busy (in flight), and result.
	let showHistory = $state(false);
	let historyOpen = $state<Record<string, boolean>>({});
	let history = $state<Record<string, Commit[]>>({});
	let historyBusy = $state<Record<string, boolean>>({});
	let historyError = $state<Record<string, string>>({});
	let revertArmed = $state<string | null>(null);
	let revertBusy = $state<string | null>(null);
	let revertResult = $state<Record<string, ProposeResult>>({});

	const total = $derived(drafts.reduce((n, d) => n + d.draft.count, 0));
	const itemKey = (p: string, ns: string, name: string) => `${p}|${ns}/${name}`;

	async function unstage(project: string, ns: string, name: string) {
		await api.unstage(ns, name);
		onchanged();
	}

	async function discardAll(project: string) {
		await api.discardDraft(project);
		onchanged();
	}

	async function propose(project: string) {
		busy[project] = true;
		error[project] = '';
		try {
			result[project] = await api.propose(project, title[project] ?? '', message[project] ?? '');
			onchanged();
		} catch (e) {
			error[project] = String(e);
		} finally {
			busy[project] = false;
		}
	}

	async function toggleHistory(project: string) {
		historyOpen[project] = !historyOpen[project];
		if (historyOpen[project] && !history[project]) await loadHistory(project);
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
			day: 'numeric'
		});
	}
</script>

<aside class="flex h-full w-[28rem] flex-col border-l border-slate-300 bg-white shadow-xl">
	<header class="flex items-center justify-between border-b border-slate-200 px-4 py-3">
		<h2 class="text-base font-semibold text-slate-800">
			Changes <span class="text-slate-400">({total})</span>
		</h2>
		<button onclick={onclose} class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		<!-- Open PRs (persistent, fetched from Forgejo) — survive closing the panel,
		     unlike the transient propose response below. -->
		{#each proposals as p (p.project)}
			<div
				class="mb-2 flex items-center gap-2 rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm"
			>
				<span class="font-medium text-slate-700">{p.project}</span>
				<span class="rounded bg-emerald-100 px-1.5 text-xs font-medium text-emerald-700">
					PR #{p.prNumber} open
				</span>
				<a
					href={p.prURL}
					target="_blank"
					rel="noopener"
					class="ml-auto min-w-0 truncate text-xs text-blue-700 underline">{p.title || p.prURL}</a
				>
			</div>
		{/each}

		<!-- Transient propose feedback. Open PRs are shown persistently above (from
		     `proposals`), so here we surface only the no-PR cases: forge unconfigured
		     (compare link) or push-only. -->
		{#each Object.entries(result).filter(([, r]) => !r.prURL) as [project, r] (project)}
			<div class="mb-2 rounded border border-green-200 bg-green-50 p-3 text-sm">
				<div class="mb-1 flex items-center gap-1">
					<span class="font-medium text-slate-700">{project}</span>
					<button
						onclick={() => delete result[project]}
						class="ml-auto text-xs text-slate-400 hover:text-slate-600">dismiss</button
					>
				</div>
				{#if r.prURL}
					<p class="text-slate-700">Pull request opened{r.existing ? ' (existing)' : ''}:</p>
					<a href={r.prURL} target="_blank" rel="noopener" class="font-medium text-blue-700 underline"
						>{r.prURL}</a
					>
				{:else if r.compareURL}
					<p class="text-slate-700">Branch <code>{r.branch}</code> pushed. Open a PR:</p>
					<a
						href={r.compareURL}
						target="_blank"
						rel="noopener"
						class="font-medium text-blue-700 underline">{r.compareURL}</a
					>
				{:else}
					<p class="text-slate-700">
						Branch <code>{r.branch}</code> pushed{r.pushed ? '' : ' (local only)'}.
					</p>
				{/if}
			</div>
		{/each}

		{#if total === 0}
			<p class="py-8 text-center text-sm text-slate-400">
				No pending changes. Edit a VM or create one to stage changes here.
			</p>
		{/if}

		{#each drafts as { project, draft } (project)}
			<section class="mb-5">
				<div class="mb-2 flex items-center gap-2">
					<Folder size={14} class="text-blue-500" />
					<span class="font-semibold text-slate-700">{project}</span>
					<span class="text-xs text-slate-400">({draft.count})</span>
					<button
						onclick={() => discardAll(project)}
						class="ml-auto text-xs text-slate-500 hover:text-slate-700">discard all</button
					>
				</div>

				{#if error[project]}
					<pre
						class="mb-2 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error[
							project
						]}</pre>
				{/if}

				{#each draft.items as item (itemKey(project, item.namespace, item.name))}
					{@const k = itemKey(project, item.namespace, item.name)}
					<div class="mb-2 rounded border border-slate-200">
						<div class="flex items-center gap-2 border-b border-slate-100 px-3 py-2">
							<span
								class="rounded px-1.5 py-0.5 text-xs {item.kind === 'delete'
									? 'bg-red-100 text-red-700'
									: item.kind === 'create'
										? 'bg-green-100 text-green-700'
										: 'bg-blue-100 text-blue-700'}">{item.kind}</span
							>
							<span class="font-medium text-slate-800">{item.namespace}/{item.name}</span>
							<button
								onclick={() => unstage(project, item.namespace, item.name)}
								class="ml-auto text-xs text-red-500 hover:text-red-700">unstage</button
							>
						</div>
						<div class="px-3 py-2">
							<ChangeList changes={item.changes} />
							{#if item.yaml}
								<button
									onclick={() => (showYaml[k] = !showYaml[k])}
									class="mt-2 flex items-center gap-1 text-xs text-slate-400 hover:text-slate-600"
								>
									{#if showYaml[k]}<ChevronDown size={12} /> hide YAML{:else}<ChevronRight size={12} /> view YAML{/if}
								</button>
								{#if showYaml[k]}
									<pre
										class="mt-1 overflow-x-auto rounded bg-slate-50 p-2 font-mono text-[11px] leading-snug text-slate-600">{item.yaml}</pre>
								{/if}
							{/if}
						</div>
					</div>
				{/each}

				<div class="mt-2 space-y-2">
					<input
						bind:value={title[project]}
						placeholder="Pull request title"
						class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"
					/>
					<textarea
						bind:value={message[project]}
						placeholder="Description (optional)"
						rows="2"
						class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"
					></textarea>
					<button
						onclick={() => propose(project)}
						disabled={busy[project]}
						class="w-full rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
					>
						{busy[project] ? 'Proposing…' : `Create pull request → ${project}`}
					</button>
				</div>
			</section>
		{/each}

		<!-- Commit history per repo-backed project, lazy-fetched on expand. Any
		     non-merge commit can be reverted as a forward-commit PR (never a rewrite). -->
		{#if projects.length > 0}
			<section class="mt-4 border-t border-slate-200 pt-3">
				<button
					onclick={() => (showHistory = !showHistory)}
					class="flex w-full items-center gap-1.5 text-sm font-semibold text-slate-700"
				>
					{#if showHistory}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
					<History size={14} class="text-slate-400" /> History
				</button>

				{#if showHistory}
					<div class="mt-2 space-y-3">
						{#each projects as project (project)}
							<div>
								<button
									onclick={() => toggleHistory(project)}
									class="flex w-full items-center gap-2 text-xs font-medium text-slate-600 hover:text-slate-800"
								>
									{#if historyOpen[project]}<ChevronDown size={12} />{:else}<ChevronRight
											size={12}
										/>{/if}
									<Folder size={12} class="text-blue-500" />
									{project}
								</button>

								{#if historyOpen[project]}
									{#if historyBusy[project]}
										<p class="px-5 py-1.5 text-xs text-slate-400">Loading…</p>
									{:else if historyError[project]}
										<p class="px-5 py-1.5 text-xs whitespace-pre-wrap text-red-600">
											{historyError[project]}
										</p>
									{:else if (history[project] ?? []).length === 0}
										<p class="px-5 py-1.5 text-xs text-slate-400">No commits.</p>
									{:else}
										<ul class="mt-1 ml-1.5 border-l border-slate-200">
											{#each history[project] as c (c.hash)}
												<li class="group py-1 pl-3">
													<div class="flex items-start gap-2">
														<div class="min-w-0 flex-1">
															<p class="truncate text-xs text-slate-700" title={c.message}>
																{c.message}
															</p>
															<p class="text-[10px] text-slate-400">
																<code class="text-slate-500">{c.shortHash}</code>
																· {c.author} ·
																<span title={c.when}>{fmtWhen(c.when)}</span>{#if c.merge} ·
																	<span class="text-slate-400">merge</span>{/if}
															</p>
														</div>
														{#if !c.merge && !revertResult[c.hash]}
															<button
																onclick={() => revert(project, c)}
																disabled={revertBusy === c.hash}
																class="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium {revertArmed ===
																	c.hash || revertBusy === c.hash
																	? 'bg-amber-100 text-amber-800'
																	: 'text-amber-700 opacity-0 hover:bg-amber-50 group-hover:opacity-100'}"
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
														{@const rr = revertResult[c.hash]}
														<p class="mt-0.5 text-[10px] text-emerald-700">
															{#if rr.prURL}
																Revert PR{rr.existing ? ' (existing)' : ''}:
																<a href={rr.prURL} target="_blank" rel="noopener" class="underline"
																	>#{rr.prNumber}</a
																>
															{:else if rr.compareURL}
																Branch <code>{rr.branch}</code> pushed —
																<a href={rr.compareURL} target="_blank" rel="noopener" class="underline"
																	>open PR</a
																>
															{:else}
																Branch <code>{rr.branch}</code> pushed.
															{/if}
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
</aside>
