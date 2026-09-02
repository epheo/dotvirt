<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import {
		ChevronDown,
		ChevronRight,
		ExternalLink,
		Folder,
		GitPullRequest,
		History,
		Pencil,
		Plus,
		Server,
		Trash2,
		TriangleAlert,
	} from 'lucide-svelte';
	import {
		api,
		type Commit,
		type DraftItem,
		type DraftView,
		type Proposal,
		type ProposeResult,
	} from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { action } from '$lib/resource.svelte';
	import { draftKindTone, TONE_PILL } from '$lib/status';
	import { drafts, PLATFORM_PROJECT } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import ChangeList from '$lib/components/ChangeList.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import GitOpsStepper from '$lib/components/GitOpsStepper.svelte';
	import Note from '$lib/components/Note.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';
	import TextInput from '$lib/components/TextInput.svelte';

	// The review workspace: everything the GitOps write model has in flight.
	// Left: staged drafts (per project), open PRs, per-project history. Right:
	// the selected change's field diff and impact, or the selected PR's review
	// state. Review happens HERE; approval and merge stay in the forge - the
	// one primary action is Propose.

	// Warning-only lanes stay: prune risk must warn BEFORE anything is staged.
	const lanes = $derived(drafts.drafts.filter((d) => d.draft.count > 0 || d.draft.warning));
	const proposals = $derived(inventory.proposals);
	// Repo-backed projects, for History (platform included for authors).
	const repoProjects = $derived(
		inventory.canManage ? [...inventory.repoProjects, PLATFORM_PROJECT] : inventory.repoProjects,
	);

	const itemKey = (it: DraftItem) => `${it.resource || 'vm'}:${it.namespace}/${it.name}`;

	type Sel =
		| { kind: 'item'; project: string; key: string }
		| { kind: 'proposal'; project: string; prNumber: number };
	let sel = $state<Sel | null>(null);

	// Resolve the selection against live data; fall back to the first staged
	// item, then the first PR (drafts and PRs both move under the page).
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
		const withItems = lanes.find((l) => l.draft.items.length > 0);
		const first = withItems?.draft.items[0];
		if (withItems && first)
			return { kind: 'item' as const, project: withItems.project, item: first };
		if (proposedLane[0]) return { kind: 'proposal' as const, proposal: proposedLane[0] };
		return null;
	});

	onMount(() => {
		untrack(() => drafts.refresh());
	});

	// ── impact: what the selected change does to the running system ─────────────
	const selectedVM = $derived.by(() => {
		if (selected?.kind !== 'item') return null;
		const { item } = selected;
		if (item.resource && item.resource !== 'vm') return null;
		return (
			inventory.allVMs.find((v) => v.namespace === item.namespace && v.name === item.name) ?? null
		);
	});

	// "+2 vCPU, +4 GiB" from the CPU/Memory change entries, when both sides parse.
	function capacityDelta(item: DraftItem): string {
		const parts: string[] = [];
		for (const c of item.changes) {
			if (c.action !== 'change' || !c.from || !c.to) continue;
			if (c.field === 'CPU') {
				const [f, t] = [c.from, c.to].map((s) => parseInt(s, 10));
				if (Number.isFinite(f) && Number.isFinite(t) && t !== f)
					parts.push(`${t > f ? '+' : ''}${t - f} vCPU`);
			} else if (c.field === 'Memory') {
				const m = (s: string) => /^(\d+(?:\.\d+)?)([KMGT]i)$/.exec(s);
				const [f, t] = [m(c.from), m(c.to)];
				if (f && t && f[2] === t[2] && f[1] !== t[1]) {
					const d = parseFloat(t[1]) - parseFloat(f[1]);
					parts.push(`${d > 0 ? '+' : ''}${d}${t[2]} memory`);
				}
			}
		}
		return parts.join(', ');
	}

	// ── propose (per project, from the selected item's lane) ─────────────────────
	let title = $state('');
	let message = $state('');
	const proposeOp = action();
	const unstageOp = action();
	const discardOp = action();
	const opError = $derived(proposeOp.error || unstageOp.error || discardOp.error);
	const clearOps = () => (proposeOp.clear(), unstageOp.clear(), discardOp.clear());
	// The propose form is per project; a selection move across projects must not
	// carry a half-typed title along.
	$effect(() => {
		selected?.kind === 'item' && selected.project;
		title = '';
		message = '';
	});

	// Propose results outlive their lane (it empties); rendered until the live
	// stream carries the PR, exactly like the drawer used to.
	let results = $state<Record<string, ProposeResult & { title?: string }>>({});
	// The PR lane: streamed proposals plus just-proposed results the stream has
	// not carried yet - same rendering, so the stream takes over invisibly.
	const proposedLane = $derived.by((): Proposal[] => {
		const out: Proposal[] = [...proposals];
		const seen = new Set(out.map((p) => `${p.project}#${p.prNumber}`));
		for (const [project, r] of Object.entries(results)) {
			if (!r.prURL || !r.prNumber || seen.has(`${project}#${r.prNumber}`)) continue;
			out.push({ project, prNumber: r.prNumber, prURL: r.prURL, title: r.title ?? '' });
		}
		return out.sort((a, b) => a.project.localeCompare(b.project));
	});
	$effect(() => {
		for (const p of proposals) {
			if (untrack(() => results[p.project])?.prNumber === p.prNumber) delete results[p.project];
		}
	});

	async function propose(project: string) {
		if (proposeOp.busy) return;
		clearOps();
		await proposeOp.run(async () => {
			const r = await api.propose(project, title, message);
			results[project] = { ...r, title: title || undefined };
			title = '';
			message = '';
		});
		// Success or failure, re-read: the push may have landed before an error.
		drafts.refresh();
	}

	async function unstage(project: string, it: DraftItem) {
		if (unstageOp.busy) return;
		clearOps();
		if (await unstageOp.run(() => api.unstage(it.namespace, it.name, it.resource, project)))
			drafts.refresh();
	}

	async function discardAll(project: string) {
		if (discardOp.busy) return;
		clearOps();
		if (await discardOp.run(() => api.discardDraft(project))) drafts.refresh();
	}

	// ── history + revert (ported from the retired drawer) ───────────────────────
	let historyOpen = $state<Record<string, boolean>>({});
	let history = $state<Record<string, Commit[]>>({});
	let historyBusy = $state<Record<string, boolean>>({});
	let historyError = $state<Record<string, string>>({});
	let revertArmed = $state<string | null>(null);
	let revertBusy = $state<string | null>(null);
	let revertResult = $state<Record<string, ProposeResult>>({});

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
			historyError[project] = friendlyError(e);
		} finally {
			historyBusy[project] = false;
		}
	}
	// Two-click revert: first arms, second opens the forward-commit PR.
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
			drafts.refresh();
		} catch (e) {
			historyError[project] = friendlyError(e);
		} finally {
			revertBusy = null;
		}
	}

	const EPOCH_FLOOR = Date.UTC(2020, 0, 1);
	function fmtWhen(iso: string): string {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t) || t < EPOCH_FLOOR) return '—';
		return new Date(t).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
		});
	}

	// Review-state summary for a PR: approvals against the branch rule when it
	// is known; zero values are unknown planes and render nothing.
	function approvalLine(p: Proposal): { tone: 'ok' | 'warn'; text: string } | null {
		const req = p.requiredApprovals ?? 0;
		const got = p.approvals ?? 0;
		if (req > 0) {
			return got >= req
				? { tone: 'ok', text: `${got}/${req} approvals` }
				: { tone: 'warn', text: `Awaiting ${req - got} approval${req - got > 1 ? 's' : ''}` };
		}
		return got > 0 ? { tone: 'ok', text: `${got} approval${got > 1 ? 's' : ''}` } : null;
	}
	function checksPill(p: Proposal): { tone: 'ok' | 'warn' | 'danger'; text: string } | null {
		switch (p.checks) {
			case 'success':
				return { tone: 'ok', text: 'Checks passed' };
			case 'pending':
				return { tone: 'warn', text: 'Checks running' };
			case 'failure':
			case 'error':
				return { tone: 'danger', text: 'Checks failed' };
		}
		return null;
	}

	const stagedTotal = $derived(lanes.reduce((n, l) => n + l.draft.count, 0));
	const laneOf = (project: string): DraftView | undefined =>
		lanes.find((l) => l.project === project)?.draft;
</script>

<svelte:head><title>Review changes — dotvirt</title></svelte:head>

<div class="flex min-h-0 flex-1 flex-col">
	<div class="flex items-baseline gap-3 border-b border-line px-4 py-3">
		<h1 class="text-lg leading-6 font-semibold text-ink">Review changes</h1>
		<span class="text-xs text-ink-faint">
			{stagedTotal} staged · {proposedLane.length} proposed. Review here; approve and merge in the forge.
			Nothing reaches the cluster until the pull request merges.
		</span>
	</div>

	<div class="flex min-h-0 flex-1">
		<!-- ── left: staged / proposed / history ─────────────────────────────── -->
		<div class="flex w-96 shrink-0 flex-col overflow-y-auto border-r border-line">
			<div class="px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase">
				Staged
			</div>
			{#if !drafts.loaded}
				<div class="space-y-2 p-3">
					{#each Array(3) as _, i (i)}
						<div class="h-7 animate-pulse rounded bg-inset-strong"></div>
					{/each}
				</div>
			{:else if lanes.length === 0}
				<p class="px-3 py-2 text-xs text-ink-faint">
					Nothing staged. Edits, creates and deletes land here before becoming a pull request.
				</p>
			{/if}
			{#each lanes as { project, draft } (project)}
				<div class="flex items-center gap-2 px-3 py-1.5">
					<Folder size={14} class="text-ink-faint" />
					<span class="font-medium text-ink">{project}</span>
					<span class="text-xs text-ink-faint"
						>{draft.count} change{draft.count > 1 ? 's' : ''}</span
					>
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
					{@const active =
						selected?.kind === 'item' &&
						selected.project === project &&
						itemKey(selected.item) === itemKey(it)}
					<button
						data-project={project}
						onclick={() => (sel = { kind: 'item', project, key: itemKey(it) })}
						class="flex w-full items-center gap-2 py-1.5 pr-3 pl-7 text-left hover:bg-select-soft {active
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
				class="mt-3 border-t border-line px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase"
			>
				Proposed
			</div>
			{#if proposedLane.length === 0}
				<p class="px-3 py-2 text-xs text-ink-faint">No open pull requests.</p>
			{/if}
			{#each proposedLane as p (p.project + '#' + p.prNumber)}
				{@const active =
					selected?.kind === 'proposal' &&
					selected.proposal.project === p.project &&
					selected.proposal.prNumber === p.prNumber}
				{@const appr = approvalLine(p)}
				{@const chk = checksPill(p)}
				<button
					onclick={() => (sel = { kind: 'proposal', project: p.project, prNumber: p.prNumber })}
					class="w-full px-3 py-2 text-left hover:bg-select-soft {active
						? 'bg-select hover:bg-select'
						: ''}"
				>
					<div class="flex items-center gap-2">
						<GitPullRequest size={13} class="shrink-0 text-accent-ink" />
						<span class="text-[13px] font-medium text-ink">PR #{p.prNumber}</span>
						<span class="text-xs text-ink-muted">{p.project}</span>
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
					<button
						onclick={() => delete results[project]}
						class="ml-1 text-ink-faint hover:text-ink-soft">dismiss</button
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
					<button
						onclick={() => toggleHistory(project)}
						class="flex w-full items-center gap-2 px-3 py-1 text-left text-[13px] text-ink-soft hover:bg-select-soft"
					>
						{#if historyOpen[project]}<ChevronDown size={12} class="text-ink-faint" />
						{:else}<ChevronRight size={12} class="text-ink-faint" />{/if}
						<History size={12} class="text-ink-faint" />
						{project}
					</button>
					{#if historyOpen[project]}
						{#if historyBusy[project]}
							<p class="py-1 pl-10 text-xs text-ink-faint">loading…</p>
						{:else if historyError[project]}
							<p class="py-1 pl-10 text-xs text-danger-ink">{historyError[project]}</p>
						{:else}
							{#each history[project] ?? [] as c (c.hash)}
								<div class="flex items-baseline gap-2 py-0.5 pr-3 pl-10 text-xs">
									<code class="shrink-0 text-ink-faint">{c.shortHash}</code>
									<span class="min-w-0 truncate text-ink-soft" title={c.message}>{c.message}</span>
									<span class="ml-auto shrink-0 text-ink-faint">{fmtWhen(c.when)}</span>
									{#if revertResult[c.hash]?.prURL}
										<a
											href={revertResult[c.hash].prURL}
											target="_blank"
											rel="noopener"
											class="shrink-0 text-accent-ink underline"
											>PR #{revertResult[c.hash].prNumber}</a
										>
									{:else if !c.merge}
										<button
											onclick={() => revert(project, c)}
											disabled={revertBusy === c.hash}
											class="shrink-0 {revertArmed === c.hash
												? 'font-medium text-danger-ink'
												: 'text-ink-faint hover:text-ink-soft'}"
											>{revertBusy === c.hash
												? 'reverting…'
												: revertArmed === c.hash
													? 'confirm revert'
													: 'revert'}</button
										>
									{/if}
								</div>
							{:else}
								<p class="py-1 pl-10 text-xs text-ink-faint">no commits</p>
							{/each}
						{/if}
					{/if}
				{:else}
					<p class="px-3 py-1 text-xs text-ink-faint">No repo-backed projects.</p>
				{/each}
			</div>
		</div>

		<!-- ── right: the selected change or PR ───────────────────────────────── -->
		<div class="flex min-w-0 flex-1 flex-col overflow-y-auto">
			{#if selected?.kind === 'item'}
				{@const it = selected.item}
				{@const project = selected.project}
				{@const restart = it.changes.filter((c) => c.restart)}
				{@const delta = capacityDelta(it)}
				<div class="flex items-center gap-2.5 border-b border-line px-4 py-3">
					<Server size={16} class="text-ink-muted" />
					<span class="text-[15px] font-semibold text-ink">{it.namespace}/{it.name}</span>
					<span class="rounded px-1.5 py-0.5 text-xs {TONE_PILL[draftKindTone(it.kind)]}"
						>{it.kind}</span
					>
					<button
						onclick={() => unstage(project, it)}
						disabled={unstageOp.busy}
						class="ml-auto text-xs text-danger hover:text-danger-ink disabled:text-ink-faint"
						>{unstageOp.busy ? 'unstaging…' : 'unstage'}</button
					>
				</div>
				<div class="space-y-3 p-4">
					<section class="rounded border border-line">
						<div class="border-b border-line bg-inset px-3 py-1.5">
							<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">
								Changes against git main
							</h3>
						</div>
						<div class="px-3 py-2">
							<ChangeList changes={it.changes} />
						</div>
					</section>

					<section class="rounded border border-line">
						<div class="border-b border-line bg-inset px-3 py-1.5">
							<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Impact</h3>
						</div>
						<div class="space-y-2 px-3 py-2 text-xs text-ink-soft">
							{#if it.kind === 'delete'}
								<p class="flex items-start gap-2">
									<TriangleAlert size={14} class="mt-0.5 shrink-0 text-danger-ink" />
									<span>
										Merging removes this VM from git; ArgoCD then deletes it from the cluster
										{#if selectedVM?.power === 'On'}<b class="font-medium">
												— it is running now{selectedVM.nodeName
													? ` on ${selectedVM.nodeName}`
													: ''}</b
											>{/if}.
									</span>
								</p>
							{:else if it.kind === 'create'}
								<p>ArgoCD creates this object after the pull request merges.</p>
							{:else if restart.length > 0}
								<p class="flex items-start gap-2">
									<TriangleAlert size={14} class="mt-0.5 shrink-0 text-warn-ink" />
									<span>
										<b class="font-medium">Restart required:</b>
										{restart.map((c) => c.field).join(', ')} appl{restart.length > 1 ? 'y' : 'ies'} at
										the next power cycle{#if selectedVM?.power === 'On'}; the VM keeps running until
											you restart it{/if}.
									</span>
								</p>
							{:else}
								<p>Applies without a restart once the pull request merges and syncs.</p>
							{/if}
							{#if delta && selectedVM?.nodeName}
								<p>
									Capacity: {delta} on <b class="font-medium">{selectedVM.nodeName}</b> once applied.
								</p>
							{/if}
						</div>
					</section>

					{#if it.yaml}
						<details class="rounded border border-line">
							<summary
								class="cursor-pointer border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
								>Manifest</summary
							>
							<pre
								class="overflow-x-auto p-3 font-mono text-[11px] leading-snug text-ink-soft">{it.yaml}</pre>
						</details>
					{/if}
				</div>

				<!-- propose footer: the one primary action -->
				{@const lane = laneOf(project)}
				<div class="mt-auto border-t border-line bg-inset px-4 py-3">
					<ErrorNote error={opError} class="mb-2" />
					<div class="mb-2 flex items-center gap-3">
						<GitOpsStepper stage="staged" />
						<span class="ml-auto text-[11px] text-ink-faint">
							Opens one pull request for the {lane?.count ?? 0} staged change{(lane?.count ?? 0) > 1
								? 's'
								: ''} in {project}. The summary and impact become the PR description.
						</span>
					</div>
					<div class="flex items-start gap-2">
						<TextInput bind:value={title} placeholder="Pull request title" class="flex-1" />
						<button
							onclick={() => propose(project)}
							disabled={proposeOp.busy}
							class="rounded-full bg-accent px-4 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:bg-line-strong"
						>
							{proposeOp.busy ? 'Proposing…' : 'Propose pull request'}
						</button>
					</div>
					<textarea
						bind:value={message}
						placeholder="Description (optional — the semantic summary is appended)"
						rows="2"
						class="mt-2 w-full rounded border border-line-strong px-2 py-1.5 text-sm"></textarea>
				</div>
			{:else if selected?.kind === 'proposal'}
				{@const p = selected.proposal}
				{@const appr = approvalLine(p)}
				{@const chk = checksPill(p)}
				<div class="flex items-center gap-2.5 border-b border-line px-4 py-3">
					<GitPullRequest size={16} class="text-accent-ink" />
					<span class="text-[15px] font-semibold text-ink">PR #{p.prNumber}</span>
					<span class="text-xs text-ink-faint">{p.project}</span>
				</div>
				<div class="space-y-3 p-4">
					{#if p.title}<p class="text-sm text-ink">{p.title}</p>{/if}
					<div class="flex items-center gap-2">
						{#if chk}<StatusPill tone={chk.tone} label={chk.text} />{/if}
						{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
					</div>
					<GitOpsStepper stage="proposed" prNumber={p.prNumber} prUrl={p.prURL} />
					<Note tone="neutral" class="max-w-xl">
						Approval and merge happen in the forge; once merged, ArgoCD applies the change and the
						result shows here and in Recent tasks within seconds.
					</Note>
					<a
						href={p.prURL}
						target="_blank"
						rel="noopener"
						class="inline-flex items-center gap-1.5 rounded border border-line-strong bg-panel px-3 py-1.5 text-sm font-medium text-accent-ink hover:bg-select-soft"
					>
						Open PR to approve and merge <ExternalLink size={13} />
					</a>
				</div>
			{:else}
				<div class="flex flex-1 items-center justify-center">
					<div class="max-w-md text-center text-sm text-ink-faint">
						<p class="mb-1 font-medium text-ink-soft">Nothing in flight.</p>
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
