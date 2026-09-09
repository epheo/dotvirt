<script lang="ts">
	import { untrack } from 'svelte';
	import { GitPullRequest, History, Pencil, RotateCcw } from 'lucide-svelte';
	import { api, type Commit, type CommitDetail, type DraftItem, type VM } from '$lib/api';
	import { friendlyError, relativeAge } from '$lib/format';
	import { changesHref } from '$lib/nav';
	import { action, resource } from '$lib/resource.svelte';
	import { approvalLine, checksPill, itemKey, reviewURL } from '$lib/review';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ChangeList from './ChangeList.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import InfoCard from './InfoCard.svelte';
	import ManifestDiff from './ManifestDiff.svelte';
	import Note from './Note.svelte';
	import StatusPill from './StatusPill.svelte';

	// The VM's Changes tab: the same lifecycle the Changes section shows, for
	// one object. Staged is this VM's item in the caller's project draft (the
	// draft proposes as a whole); Proposed lists the open PRs whose diff
	// touches this VM; History is every version of its manifest, each
	// restorable. Restore is the object-level undo: it stages one file as a
	// past commit held it and goes through Propose like any edit. Undo of a
	// whole pull request stays in the Changes section, at the project.
	let {
		vm,
		stagedItem,
		onstaged,
	}: { vm: VM; stagedItem: DraftItem | null; onstaged?: () => void } = $props();

	const project = $derived(inventory.projectOf(vm.namespace));
	const draftCount = $derived(drafts.drafts.find((d) => d.project === project)?.draft.count ?? 0);

	// History keys on the project's applied revision: a merge that reaches the
	// cluster moves it, which is when a new version exists to show.
	const revision = $derived(
		inventory.inventory?.projects.find((p) => p.name === project)?.gitOps?.revision ?? '',
	);
	const historyRes = resource<Commit[]>(
		() => `${vm.namespace}/${vm.name}|${vm.sourceFile ?? ''}|${revision}`,
		() => (vm.sourceFile ? api.vmHistory(vm.namespace, vm.name) : Promise.resolve([])),
		{ reset: true },
	);
	const commits = $derived(historyRes.data ?? []);

	// Open PRs of the project, kept when their diff names this VM. A PR's
	// items load once (the same review the Changes section renders).
	const projectPRs = $derived(inventory.proposals.filter((p) => p.project === project));
	let prItems = $state<Record<number, DraftItem[] | null>>({});
	$effect(() => {
		for (const p of projectPRs) {
			if (untrack(() => prItems[p.prNumber]) !== undefined) continue;
			prItems[p.prNumber] = null;
			api
				.proposal(p.project, p.prNumber)
				.then((d) => (prItems[p.prNumber] = d.items))
				.catch(() => (prItems[p.prNumber] = []));
		}
	});
	const touching = $derived(
		projectPRs.filter((p) =>
			(prItems[p.prNumber] ?? []).some(
				(it) =>
					(!it.resource || it.resource === 'vm') &&
					it.namespace === vm.namespace &&
					it.name === vm.name,
			),
		),
	);
	const prsLoading = $derived(projectPRs.some((p) => prItems[p.prNumber] === null));

	// One version's diff, opened inline: the commit's items narrowed to this VM.
	let open = $state<string | null>(null);
	let details = $state<Record<string, CommitDetail>>({});
	let detailError = $state<Record<string, string>>({});
	async function toggle(hash: string) {
		open = open === hash ? null : hash;
		if (!open || details[hash] || detailError[hash]) return;
		try {
			details[hash] = await api.commit(project, hash);
		} catch (e) {
			detailError[hash] = friendlyError(e);
		}
	}
	const ownItems = (d: CommitDetail): DraftItem[] => {
		const mine = d.items.filter((it) => it.namespace === vm.namespace && it.name === vm.name);
		return mine.length ? mine : d.items;
	};

	// Restore stages, nothing more: the draft is where it can still be
	// unstaged, and the PR is where it is approved.
	const restoreOp = action();
	let restoring = $state<string | null>(null);
	async function restore(c: Commit) {
		if (restoreOp.busy) return;
		restoring = c.hash;
		const ok = await restoreOp.run(() => api.restoreVersion(vm.namespace, vm.name, c.hash));
		restoring = null;
		if (!ok) return;
		await drafts.refresh();
		onstaged?.();
		ui.showToast(`Restore of ${vm.name} to ${c.shortHash} staged.`, {
			kind: 'success',
			action: {
				label: 'Review & propose',
				run: () => ui.openChanges({ kind: 'history', project }),
			},
		});
	}
</script>

<div class="max-w-3xl space-y-4">
	{#if !vm.sourceFile}
		<Note tone="warn">
			{vm.name} is not tracked in git, so it has no change history. Adopt it from the Summary tab to bring
			it under GitOps.
		</Note>
	{:else}
		<InfoCard title="Staged">
			{#snippet action()}
				{#if stagedItem}
					<a
						href={reviewURL({ kind: 'item', project, key: itemKey(stagedItem) })}
						class="text-xs text-accent-ink hover:underline">Review and propose</a
					>
				{/if}
			{/snippet}
			{#if stagedItem}
				<div
					class="flex items-center gap-2 border-b border-line-soft px-3 py-1.5 text-xs text-ink-muted"
				>
					<Pencil size={12} class="text-accent-ink" />
					<span class="capitalize">{stagedItem.kind}</span>
					<span class="ml-auto"
						>part of your {project} draft ({draftCount} item{draftCount === 1 ? '' : 's'})</span
					>
				</div>
				<div class="px-3 py-2">
					<ChangeList changes={stagedItem.changes} />
				</div>
				<p class="border-t border-line-soft bg-inset px-3 py-1.5 text-xs text-ink-faint">
					Propose sends the whole {project} draft as one pull request.
				</p>
			{:else}
				<p class="px-3 py-2 text-xs text-ink-faint">
					Nothing staged for this VM. Edit Settings, power and restore all land here first.
				</p>
			{/if}
		</InfoCard>

		<InfoCard title="Proposed">
			{#snippet action()}
				<span class="text-xs text-ink-faint">pull requests touching {vm.name}</span>
			{/snippet}
			{#if touching.length === 0}
				<p class="px-3 py-2 text-xs text-ink-faint">
					{prsLoading ? 'Checking open pull requests…' : 'No open pull request touches this VM.'}
				</p>
			{:else}
				<ul class="divide-y divide-line-soft text-[13px]">
					{#each touching as p (p.prNumber)}
						{@const appr = approvalLine(p)}
						{@const chk = checksPill(p)}
						<li class="flex items-center gap-2 px-3 py-1.5">
							<GitPullRequest size={13} class="shrink-0 text-accent-ink" />
							<span class="font-medium text-ink">PR #{p.prNumber}</span>
							{#if p.by}<span class="text-xs text-ink-faint">{p.mine ? 'yours' : `by ${p.by}`}</span
								>{/if}
							<span class="min-w-0 truncate text-ink-soft">{p.title}</span>
							<span class="ml-auto flex shrink-0 items-center gap-1.5">
								{#if chk}<StatusPill tone={chk.tone} label={chk.text} />{/if}
								{#if appr}<StatusPill tone={appr.tone} label={appr.text} />{/if}
								<a
									href={reviewURL({ kind: 'proposal', project, prNumber: p.prNumber })}
									class="text-xs text-accent-ink hover:underline">Review</a
								>
							</span>
						</li>
					{/each}
				</ul>
			{/if}
		</InfoCard>

		<InfoCard title="History">
			{#snippet action()}
				<span class="text-xs text-ink-faint">every version of {vm.name} in git, newest first</span>
			{/snippet}
			<ErrorNote error={restoreOp.error} class="m-3" />
			{#if historyRes.loading}
				<p class="px-3 py-2 text-xs text-ink-faint">loading…</p>
			{:else if historyRes.failed}
				<p class="px-3 py-2 text-xs text-danger-ink">{historyRes.error}</p>
			{:else if commits.length === 0}
				<p class="px-3 py-2 text-xs text-ink-faint">No merged changes yet.</p>
			{:else}
				<ul class="divide-y divide-line-soft text-[13px]">
					{#each commits as c, i (c.hash)}
						{@const current = i === 0}
						{@const shown = open === c.hash}
						<li>
							<div class="flex items-center gap-3 px-3 py-2 {shown ? 'bg-select-soft' : ''}">
								<span
									class="h-2.5 w-2.5 shrink-0 rounded-full border-2 border-accent {current
										? 'bg-accent'
										: 'bg-panel'}"
								></span>
								<a
									href={reviewURL({ kind: 'commit', project, hash: c.hash })}
									class="min-w-0 truncate font-medium text-ink hover:text-accent-ink"
									title="Review this change at the project, with Undo"
								>
									{c.title}{#if c.prNumber}<span class="ml-1 font-normal text-ink-faint"
											>#{c.prNumber}</span
										>{/if}
								</a>
								{#if current}<span class="shrink-0 text-xs text-ink-faint">· current</span>{/if}
								<span class="ml-auto shrink-0 text-xs text-ink-faint"
									>{relativeAge(c.when)} · {c.author}</span
								>
								<button
									onclick={() => toggle(c.hash)}
									class="shrink-0 text-xs text-accent-ink hover:underline"
									>{shown ? 'Hide diff' : 'Diff'}</button
								>
								{#if current}
									<span
										class="shrink-0 rounded border border-line px-2 py-0.5 text-xs text-ink-faint"
										>Current version</span
									>
								{:else}
									<button
										onclick={() => restore(c)}
										disabled={restoreOp.busy}
										class="inline-flex shrink-0 items-center gap-1 rounded border border-line-strong bg-panel px-2 py-0.5 text-xs font-medium text-ink hover:bg-select-soft disabled:text-ink-faint"
									>
										<RotateCcw size={11} />
										{restoring === c.hash ? 'Restoring…' : 'Restore this version'}
									</button>
								{/if}
							</div>
							{#if shown}
								{@const d = details[c.hash]}
								<div class="space-y-2 border-t border-line-soft bg-inset px-3 py-2 pl-9">
									{#if detailError[c.hash]}
										<ErrorNote error={detailError[c.hash]} />
									{:else if !d}
										<div class="h-10 animate-pulse rounded bg-inset-strong"></div>
									{:else}
										{#each ownItems(d) as it (itemKey(it))}
											<ChangeList changes={it.changes} />
											{#if it.yaml}
												<details class="rounded border border-line bg-panel">
													<summary
														class="cursor-pointer px-3 py-1 text-xs font-semibold tracking-wide text-ink-muted uppercase"
													>
														{it.baseYAML ? 'Manifest diff' : 'Manifest'}
													</summary>
													<ManifestDiff before={it.baseYAML} after={it.yaml} />
												</details>
											{/if}
										{:else}
											<p class="text-xs text-ink-faint">This commit changed no manifests.</p>
										{/each}
										{#if !current}
											<p class="text-xs text-ink-faint">
												Restoring stages {vm.name} exactly as it was after this change into your
												{project} draft. Nothing changes until that pull request merges.
											</p>
										{/if}
									{/if}
								</div>
							{/if}
						</li>
					{/each}
				</ul>
				<p
					class="flex items-center gap-2 border-t border-line-soft bg-inset px-3 py-1.5 text-xs text-ink-faint"
				>
					<History size={12} />
					<span>
						Showing {commits.length} version{commits.length === 1 ? '' : 's'}. Undo of a whole pull
						request lives under
						<a href={changesHref(project)} class="text-accent-ink hover:underline">Changes</a>, at
						the project.
					</span>
				</p>
			{/if}
		</InfoCard>
	{/if}
</div>
