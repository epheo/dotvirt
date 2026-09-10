<script lang="ts">
	import { History, RotateCcw } from 'lucide-svelte';
	import { api, type Commit, type CommitDetail, type DraftItem } from '$lib/api';
	import { friendlyError, relativeAge } from '$lib/format';
	import { changesHref } from '$lib/nav';
	import { action, resource } from '$lib/resource.svelte';
	import { itemKey, reviewURL } from '$lib/review';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ChangeList from './ChangeList.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import InfoCard from './InfoCard.svelte';
	import ManifestDiff from './ManifestDiff.svelte';

	// One object's manifest history in git, each version restorable: the VM
	// page's History card, shared with segments, rules and uplinks. Restore is
	// the object-level undo: it stages one file as a past commit held it and
	// goes through Propose like any edit. Undo of a whole pull request stays in
	// the Changes section, at the project.
	let {
		project,
		name,
		load,
		restore: restoreVersion,
		mine,
		onstaged,
	}: {
		project: string;
		name: string; // the object, for copy
		load: () => Promise<Commit[]>;
		restore: (hash: string) => Promise<unknown>;
		mine: (it: DraftItem) => boolean; // a commit's items that belong to this object
		onstaged?: () => void;
	} = $props();

	// History keys on the project's applied revision: a merge that reaches the
	// cluster moves it, which is when a new version exists to show.
	const revision = $derived(
		inventory.inventory?.projects.find((p) => p.name === project)?.gitOps?.revision ?? '',
	);
	const historyRes = resource<Commit[]>(
		() => `${project}/${name}|${revision}`,
		() => load(),
		{
			reset: true,
		},
	);
	const commits = $derived(historyRes.data ?? []);

	// One version's diff, opened inline: the commit's items narrowed to this object.
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
		const own = d.items.filter(mine);
		return own.length ? own : d.items;
	};

	// Restore stages, nothing more: the draft is where it can still be
	// unstaged, and the PR is where it is approved.
	const restoreOp = action();
	let restoring = $state<string | null>(null);
	async function restore(c: Commit) {
		if (restoreOp.busy) return;
		restoring = c.hash;
		const ok = await restoreOp.run(() => restoreVersion(c.hash));
		restoring = null;
		if (!ok) return;
		await drafts.refresh();
		onstaged?.();
		ui.showToast(`Restore of ${name} to ${c.shortHash} staged.`, {
			kind: 'success',
			action: {
				label: 'Review & propose',
				run: () => ui.openChanges({ kind: 'history', project }),
			},
		});
	}
</script>

<InfoCard title="History">
	{#snippet action()}
		<span class="text-xs text-ink-faint">every version of {name} in git, newest first</span>
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
							<span class="shrink-0 rounded border border-line px-2 py-0.5 text-xs text-ink-faint"
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
										Restoring stages {name} exactly as it was after this change into your
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
				<a href={changesHref(project)} class="text-accent-ink hover:underline">Changes</a>, at the
				project.
			</span>
		</p>
	{/if}
</InfoCard>
