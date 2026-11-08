<script lang="ts">
	import { page } from '$app/state';
	import { Folder, GitPullRequest, Layers, Network, Pencil } from '@lucide/svelte';
	import type { Project, ProjectNamespace } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { changesHref, hrefForScope } from '#lib/nav.js';
	import {
		bySize,
		fold,
		LEAF_LIMIT,
		leafWindow,
		matches,
		SMALL_FLEET,
		TOP_GROUPS,
	} from '#lib/rail.js';
	import { drafts, PLATFORM_PROJECT } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import RailFilter from './RailFilter.svelte';
	import RailMore from './RailMore.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Changes tree: the compute hierarchy as a scope selector for the review
	// workspace - a project or namespace scopes the page, a VM opens its own
	// Changes tab (the VM route keeps this tree, like every other). Badges say
	// what is in flight per project: staged edits and open pull requests.
	// Projects with something in flight always show; the rest rank by size and
	// fold into "N more", and an open namespace lists its first VMs. Projects
	// without a repo have no changes to review and sit dimmed below.
	const scope = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		if (parts[0] !== 'changes') return { project: '', namespace: '' };
		return { project: parts[1] ?? '', namespace: parts[2] ?? '' };
	});
	// The VM page in view (this tree stays on it): its project and namespace
	// open so its leaf is on screen.
	const vmInView = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		return parts[0] === 'vm' && parts.length >= 3 ? { namespace: parts[1], name: parts[2] } : null;
	});
	const projects = $derived(inventory.projects);
	const small = $derived(inventory.vmCount <= SMALL_FLEET);
	const fmt = (n: number) => n.toLocaleString('en-US');

	const stagedIn = (project: string) =>
		drafts.drafts.find((d) => d.project === project)?.draft.count ?? 0;
	const stagedInNS = (project: string, ns: string) =>
		drafts.drafts
			.find((d) => d.project === project)
			?.draft.items.filter((it) => it.namespace === ns).length ?? 0;
	const openIn = (project: string) =>
		inventory.proposals.filter((p) => p.project === project).length;
	const inFlight = (p: Project) => stagedIn(p.name) > 0 || openIn(p.name) > 0;
	const holdsVM = (p: Project) =>
		!!vmInView && p.namespaces.some((n) => n.namespace === vmInView.namespace);

	let query = $state('');
	let unfolded = $state(false);
	const filtering = $derived(query.trim() !== '');
	const vmCount = (p: Project) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0);
	const projectMatches = (p: Project) =>
		matches(query, p.name, ...p.namespaces.map((n) => n.namespace));
	// A query that names a namespace, not its project, narrows the project to
	// the matching namespaces and opens it.
	const byNamespace = (p: Project) => filtering && !matches(query, p.name);
	const nsShown = (p: Project) =>
		byNamespace(p) ? p.namespaces.filter((n) => matches(query, n.namespace)) : p.namespaces;

	// In flight first, then the biggest.
	const ranked = $derived.by(() => {
		const all = bySize(
			projects.filter((p) => p.repo && projectMatches(p)),
			vmCount,
			(p) => p.name,
		);
		return [...all.filter(inFlight), ...all.filter((p) => !inFlight(p))];
	});
	const folding = $derived(
		filtering || unfolded || small
			? { shown: ranked, folded: [] as Project[] }
			: fold(ranked, TOP_GROUPS, (p) => inFlight(p) || p.name === scope.project || holdsVM(p)),
	);
	const untracked = $derived(projects.filter((p) => !p.repo && projectMatches(p)));
	// A stager without platform read still reaches the PRs they opened there.
	const showPlatform = $derived(
		(clusterGate.platform().ok ||
			clusterGate.platformRead().ok ||
			inventory.proposals.some((p) => p.project === PLATFORM_PROJECT && p.mine)) &&
			matches(query, PLATFORM_PROJECT),
	);

	// Collapsed state keyed by node id, persisted. Unset means the default: open
	// in a small fleet, on the path to what is in view, or when a filter matched
	// inside. An explicit toggle wins either way.
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.changes', {});
	const isOpen = (id: string, byDefault: boolean) => {
		const v = collapsed.value[id];
		return v === undefined ? byDefault : !v;
	};
	const setOpen = (id: string, open: boolean) =>
		(collapsed.value = { ...collapsed.value, [id]: !open });
	const projectOpen = (p: Project) =>
		isOpen(`p:${p.name}`, small || p.name === scope.project || holdsVM(p) || byNamespace(p));
	const nsOpen = (p: Project, ns: ProjectNamespace) =>
		isOpen(
			`n:${p.name}/${ns.namespace}`,
			small || ns.namespace === scope.namespace || ns.namespace === vmInView?.namespace,
		);

	const HEAD = 'px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase';
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
	{@const open = projectOpen(p)}
	<TreeRow
		active={scope.project === p.name && !scope.namespace}
		expanded={open}
		ontoggle={() => setOpen(pid, !open)}
		href={changesHref(p.name)}
	>
		{#snippet icon()}<Folder size={14} class="shrink-0 text-side-dim" />{/snippet}
		<span class="truncate {open ? 'text-side-ink' : ''}">{p.name}</span>
		{#snippet trailing()}{@render badges(stagedIn(p.name), openIn(p.name))}{/snippet}
	</TreeRow>
	{#if open}
		{#each nsShown(p) as ns (ns.namespace)}
			{@const nid = `n:${p.name}/${ns.namespace}`}
			{@const nsIsOpen = nsOpen(p, ns)}
			{@const leaves = leafWindow(
				ns.vms,
				LEAF_LIMIT,
				(v) => v.namespace === vmInView?.namespace && v.name === vmInView?.name,
			)}
			<TreeRow
				indent={1}
				active={scope.project === p.name && scope.namespace === ns.namespace}
				expanded={nsIsOpen}
				ontoggle={() => setOpen(nid, !nsIsOpen)}
				href={changesHref(p.name, ns.namespace)}
			>
				{#snippet icon()}<Layers size={13} class="shrink-0 text-side-dim" />{/snippet}
				<span class="truncate {nsIsOpen ? 'text-side-ink' : ''}">{ns.namespace}</span>
				{#snippet trailing()}{@render badges(stagedInNS(p.name, ns.namespace), 0)}{/snippet}
			</TreeRow>
			{#if nsIsOpen}
				{#each leaves.shown as vm (vm.namespace + '/' + vm.name)}
					<TreeVMRow {vm} indent={3} tab="changes" />
				{/each}
				{#if leaves.more > 0}
					<RailMore
						href="{hrefForScope({
							kind: 'namespace',
							project: p.name,
							namespace: ns.namespace,
						})}?tab=vms"
						indent={3}>{fmt(leaves.more)} more VMs</RailMore
					>
				{/if}
			{/if}
		{/each}
	{/if}
{/snippet}

<div class="flex flex-col gap-0.5 px-2.5 pb-3 text-[13px] select-none">
	<RailFilter
		bind:value={query}
		label="Filter projects"
		placeholder="Filter {projects.length} projects"
	/>

	<TreeRow active={page.url.pathname === '/changes'} href="/changes">
		<span class="truncate text-side-ink">All changes</span>
		{#snippet trailing()}{@render badges(
				drafts.drafts.reduce((n, d) => n + d.draft.count, 0),
				inventory.proposals.length,
			)}{/snippet}
	</TreeRow>

	{#if folding.shown.length > 0 || showPlatform}
		<div class={HEAD}>Projects</div>
	{/if}

	{#each folding.shown as p (p.name)}
		{@render projectRow(p)}
	{/each}

	{#if folding.folded.length > 0}
		<RailMore onclick={() => (unfolded = true)}>
			{folding.folded.length} more projects
		</RailMore>
	{:else if unfolded && !filtering && !small}
		<RailMore onclick={() => (unfolded = false)}>Show the busiest only</RailMore>
	{/if}

	<!-- A stager reaches their own draft here; a reader the tier's PRs and history. -->
	{#if showPlatform}
		<TreeRow
			active={scope.project === PLATFORM_PROJECT}
			alignChevron
			href={changesHref(PLATFORM_PROJECT)}
		>
			{#snippet icon()}<Network size={14} class="shrink-0 text-side-dim" />{/snippet}
			<span class="truncate">platform</span>
			{#snippet trailing()}{@render badges(
					stagedIn(PLATFORM_PROJECT),
					openIn(PLATFORM_PROJECT),
				)}{/snippet}
		</TreeRow>
	{/if}

	{#if untracked.length > 0}
		<div class="mt-2 border-t border-side-hover {HEAD}">Untracked, no repo</div>
		{#each untracked as p (p.name)}
			<TreeRow active={scope.project === p.name} alignChevron href={changesHref(p.name)}>
				{#snippet icon()}<Folder size={14} class="shrink-0 text-side-dim" />{/snippet}
				<span class="truncate text-side-dim italic">{p.name}</span>
				{#snippet trailing()}<span class="text-xs text-side-dim">no repo</span>{/snippet}
			</TreeRow>
		{/each}
	{/if}

	{#if filtering && folding.shown.length === 0 && untracked.length === 0 && !showPlatform}
		<div class="px-2 py-3 text-xs text-side-dim">No project or namespace matches.</div>
	{:else if projects.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No projects visible.</div>
	{/if}
</div>
