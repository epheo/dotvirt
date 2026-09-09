<script lang="ts">
	import { page } from '$app/state';
	import { Folder, GitPullRequest, Layers, LayoutGrid, Network, Pencil } from 'lucide-svelte';
	import type { Project } from '$lib/api';
	import { changesHref } from '$lib/nav';
	import { drafts, PLATFORM_PROJECT } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { persisted } from '$lib/state/persisted.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Changes tree: the compute hierarchy as a scope selector for the review
	// workspace - a project or namespace scopes the page, a VM opens its own
	// Changes tab (the VM route keeps this tree, like every other). Badges say
	// what is in flight per project: staged edits and open pull requests.
	// Projects without a repo have no changes to review and sit dimmed.
	const scope = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		if (parts[0] !== 'changes') return { project: '', namespace: '' };
		return { project: parts[1] ?? '', namespace: parts[2] ?? '' };
	});
	const projects = $derived(inventory.inventory?.projects ?? []);
	const tracked = $derived(projects.filter((p) => p.repo));
	const untracked = $derived(projects.filter((p) => !p.repo));

	const stagedIn = (project: string) =>
		drafts.drafts.find((d) => d.project === project)?.draft.count ?? 0;
	const stagedInNS = (project: string, ns: string) =>
		drafts.drafts
			.find((d) => d.project === project)
			?.draft.items.filter((it) => it.namespace === ns).length ?? 0;
	const openIn = (project: string) =>
		inventory.proposals.filter((p) => p.project === project).length;

	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.changes', {});
	const toggle = (id: string) =>
		(collapsed.value = { ...collapsed.value, [id]: !collapsed.value[id] });
	const open = (id: string) => !collapsed.value[id];
</script>

{#snippet badges(staged: number, prs: number)}
	{#if staged > 0}
		<span
			class="inline-flex items-center gap-0.5 rounded bg-accent-soft px-1 text-[10px] font-medium text-accent-ink"
			title="{staged} staged"><Pencil size={10} /> {staged}</span
		>
	{/if}
	{#if prs > 0}
		<span
			class="inline-flex items-center gap-0.5 rounded bg-side-active px-1 text-[10px] font-medium text-side-ink"
			title="{prs} open pull request{prs === 1 ? '' : 's'}"><GitPullRequest size={10} /> {prs}</span
		>
	{/if}
{/snippet}

{#snippet projectRow(p: Project)}
	{@const pid = `p:${p.name}`}
	<div>
		<TreeRow
			active={scope.project === p.name && !scope.namespace}
			expanded={open(pid)}
			ontoggle={() => toggle(pid)}
			href={changesHref(p.name)}
		>
			{#snippet icon()}<Folder size={14} class="shrink-0 text-side-dim" />{/snippet}
			<span class="truncate font-semibold text-side-ink">{p.name}</span>
			{#snippet trailing()}{@render badges(stagedIn(p.name), openIn(p.name))}{/snippet}
		</TreeRow>
		{#if open(pid)}
			{#each p.namespaces as ns (ns.namespace)}
				{@const nid = `n:${p.name}/${ns.namespace}`}
				<TreeRow
					indent={1}
					active={scope.project === p.name && scope.namespace === ns.namespace}
					expanded={open(nid)}
					ontoggle={() => toggle(nid)}
					href={changesHref(p.name, ns.namespace)}
				>
					{#snippet icon()}<Layers size={14} class="shrink-0 text-side-dim" />{/snippet}
					<span class="truncate font-semibold text-side-ink">{ns.namespace}</span>
					{#snippet trailing()}{@render badges(stagedInNS(p.name, ns.namespace), 0)}{/snippet}
				</TreeRow>
				{#if open(nid)}
					{#each ns.vms as vm (vm.namespace + '/' + vm.name)}
						<TreeVMRow {vm} indent={3} tab="changes" />
					{/each}
				{/if}
			{/each}
		{/if}
	</div>
{/snippet}

<div class="select-none text-[13px]">
	<TreeRow active={page.url.pathname === '/changes'} alignChevron href="/changes">
		{#snippet icon()}<LayoutGrid size={14} class="text-side-dim" />{/snippet}
		<span class="truncate font-semibold text-side-ink">All changes</span>
	</TreeRow>

	{#each tracked as p (p.name)}
		{@render projectRow(p)}
	{/each}

	{#if inventory.canManage}
		<TreeRow
			active={scope.project === PLATFORM_PROJECT}
			alignChevron
			href={changesHref(PLATFORM_PROJECT)}
		>
			{#snippet icon()}<Network size={14} class="text-side-dim" />{/snippet}
			<span class="truncate font-semibold text-side-ink">platform</span>
			{#snippet trailing()}{@render badges(
					stagedIn(PLATFORM_PROJECT),
					openIn(PLATFORM_PROJECT),
				)}{/snippet}
		</TreeRow>
	{/if}

	{#each untracked as p (p.name)}
		<TreeRow active={scope.project === p.name} alignChevron href={changesHref(p.name)}>
			{#snippet icon()}<Folder size={14} class="shrink-0 text-side-dim" />{/snippet}
			<span class="truncate text-side-dim italic">{p.name}</span>
			{#snippet trailing()}<span class="text-xs text-side-dim">no repo</span>{/snippet}
		</TreeRow>
	{/each}

	{#if projects.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No projects visible.</div>
	{/if}
</div>
