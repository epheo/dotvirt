<script lang="ts">
	import { untrack } from 'svelte';
	import { GitPullRequest, Pencil } from 'lucide-svelte';
	import { api, type DraftItem, type VM } from '$lib/api';
	import { approvalLine, checksPill, itemKey, reviewURL } from '$lib/review';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ChangeList from './ChangeList.svelte';
	import InfoCard from './InfoCard.svelte';
	import FileHistory from './FileHistory.svelte';
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

		<FileHistory
			{project}
			name={vm.name}
			load={() => api.vmHistory(vm.namespace, vm.name)}
			restore={(hash) => api.restoreVersion(vm.namespace, vm.name, hash)}
			mine={(it) => it.namespace === vm.namespace && it.name === vm.name}
			{onstaged}
		/>
	{/if}
</div>
