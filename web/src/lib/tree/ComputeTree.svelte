<script lang="ts">
	import { page } from '$app/state';
	import { Folder, Layers } from '@lucide/svelte';
	import type { Project, ProjectNamespace } from '#lib/api.js';
	import { issueCountByProject } from '#lib/issues.js';
	import { hrefForScope, scopeFromPath, templatesHref } from '#lib/nav.js';
	import {
		bySize,
		fold,
		LEAF_LIMIT,
		leafWindow,
		matches,
		SMALL_FLEET,
		TOP_GROUPS,
	} from '#lib/rail.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import ProjectSyncBadge from '#lib/components/ProjectSyncBadge.svelte';
	import StatusDot from '#lib/components/StatusDot.svelte';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import RailFilter from './RailFilter.svelte';
	import RailMore from './RailMore.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Compute tree: All VMs, then projects (biggest first, the rest folded
	// into "N more"), namespaces, and at most LEAF_LIMIT VMs per namespace with
	// "N more" opening its VMs tab. Highlights derive from the URL. Projects
	// without a repo sink below the tracked ones so a broken or unadopted
	// project never pushes real work down.
	const scope = $derived(scopeFromPath(page.url.pathname));
	const projects = $derived(inventory.projects);
	const small = $derived(inventory.vmCount <= SMALL_FLEET);
	const fmt = (n: number) => n.toLocaleString('en-US');
	const onTemplates = $derived(
		page.url.pathname === '/compute' && page.url.searchParams.get('tab') === 'templates',
	);

	// The VM page in view, and the project and namespace holding it: their rows
	// open by default so the VM's leaf is on screen.
	const vmInView = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		return parts[0] === 'vm' && parts.length >= 3 ? { namespace: parts[1], name: parts[2] } : null;
	});
	const projectInView = $derived(
		scope.kind === 'project' || scope.kind === 'namespace'
			? scope.project
			: vmInView
				? projects.find((p) => p.namespaces.some((n) => n.namespace === vmInView.namespace))?.name
				: undefined,
	);
	const nsInView = $derived(scope.kind === 'namespace' ? scope.namespace : vmInView?.namespace);

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

	const ranked = $derived(
		bySize(
			projects.filter((p) => !p.error && projectMatches(p)),
			vmCount,
			(p) => p.name,
		),
	);
	const folding = $derived(
		filtering || unfolded || small
			? { shown: ranked, folded: [] as Project[] }
			: fold(ranked, TOP_GROUPS, (p) => p.name === projectInView),
	);
	const untracked = $derived(projects.filter((p) => p.error && projectMatches(p)));

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;

	// Collapsed state keyed by node id, persisted. Unset means the default: open
	// in a small fleet, on the path to what is in view, or when a filter matched
	// inside; untracked projects start closed (their warning is noise until
	// acted on). An explicit toggle wins either way.
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.compute', {});
	const isOpen = (id: string, byDefault: boolean) => {
		const v = collapsed.value[id];
		return v === undefined ? byDefault : !v;
	};
	const setOpen = (id: string, open: boolean) =>
		(collapsed.value = { ...collapsed.value, [id]: !open });
	const projectOpen = (p: Project) =>
		isOpen(`p:${p.name}`, !p.error && (small || p.name === projectInView || byNamespace(p)));
	const nsOpen = (p: Project, ns: ProjectNamespace) =>
		isOpen(`n:${p.name}/${ns.namespace}`, small || ns.namespace === nsInView);

	// Drift rolls up: a namespace flags drift if any of its VMs is OutOfSync; a
	// project flags drift if any of its namespaces does.
	const nsDrift = (ns: ProjectNamespace) => ns.vms.some((v) => v.sync === 'OutOfSync');
	const projectDrift = (p: Project) => p.namespaces.some(nsDrift);

	// Standing problems per project, for the attention dot on the row.
	type Severity = 'danger' | 'warn' | 'info';
	const RANK: Record<Severity, number> = { danger: 3, warn: 2, info: 1 };
	const worse = (a: Severity | undefined, b: Severity | undefined) =>
		!a || (b && RANK[b] > RANK[a]) ? b : a;
	const issueCounts = $derived(issueCountByProject(inventory.issues));
	const issueTone = $derived.by(() => {
		const m = new Map<string, Severity>();
		for (const i of inventory.issues) m.set(i.project, worse(m.get(i.project), i.severity)!);
		return m;
	});
	// The folded projects' worst problem rides their "N more" row.
	const foldedTone = $derived(
		folding.folded.reduce<Severity | undefined>(
			(w, p) => worse(w, issueTone.get(p.name)),
			undefined,
		),
	);

	function ctxContainer(
		e: MouseEvent,
		c: { project: string; repo?: string; namespace?: string; namespaces: string[] },
	) {
		e.preventDefault();
		ui.ctx = { x: e.clientX, y: e.clientY, kind: 'container', ...c };
	}
</script>

<div class="flex flex-col gap-0.5 px-2.5 pb-3 text-[13px] select-none">
	<RailFilter
		bind:value={query}
		label="Filter projects"
		placeholder="Filter {projects.length} projects"
	/>

	{#if vmsOff()}
		<TreeRow active={scope.kind === 'all'} href="/compute">
			<span class="truncate text-side-ink">All projects</span>
			{#snippet trailing()}<span class="text-xs text-side-dim"
					>{fmt(inventory.projects.length)}</span
				>{/snippet}
		</TreeRow>
	{:else}
		<!-- A row keeps the tab in view (keepTab), so All VMs names its tab to leave Templates. -->
		<TreeRow
			active={scope.kind === 'all' && page.url.pathname.startsWith('/compute') && !onTemplates}
			href={onTemplates ? '/compute?tab=summary' : '/compute'}
		>
			<span class="truncate text-side-ink">All VMs</span>
			{#snippet trailing()}<span class="text-xs text-side-dim">{fmt(inventory.vmCount)}</span
				>{/snippet}
		</TreeRow>
		<TreeRow active={onTemplates} href={templatesHref()}>
			<span class="truncate text-side-ink">VM templates</span>
		</TreeRow>
	{/if}

	{#if projects.length > 0}
		<div class="px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase">
			Projects
		</div>
	{/if}

	{#snippet projectNode(project: Project)}
		{@const open = projectOpen(project)}
		<!-- Project: chevron toggles collapse, the label focuses the grid. -->
		<TreeRow
			active={projectScoped(project.name)}
			expanded={open}
			ontoggle={() => setOpen(`p:${project.name}`, !open)}
			href={hrefForScope({ kind: 'project', project: project.name })}
			oncontextmenu={(e) =>
				ctxContainer(e, {
					project: project.name,
					repo: project.repo,
					namespaces: project.namespaces.map((n) => n.namespace),
				})}
		>
			{#snippet icon()}
				<Folder size={14} class="shrink-0 text-side-dim" />
			{/snippet}
			<span class="truncate {open ? 'text-side-ink' : ''}">{project.name}</span>
			<!-- The tree only marks state, one dot per row at the right: the worst
			     issue when the project has any, else the Application rollup (or the
			     VM-only drift dot when Argo isn't wired). -->
			{#snippet trailing()}
				{@const n = issueCounts.get(project.name) ?? 0}
				{#if n > 0}
					<StatusDot
						tone={issueTone.get(project.name) ?? 'warn'}
						size="xs"
						title="{n} issue{n === 1 ? '' : 's'} in this project, listed under Issues"
					/>
				{:else if !project.error}
					<ProjectSyncBadge gitOps={project.gitOps} compact />
					{#if !project.gitOps && projectDrift(project)}
						<StatusDot tone="danger" size="xs" title="A VM is out of sync" />
					{/if}
				{/if}
				<span class="text-xs text-side-dim">{fmt(vmCount(project))}</span>
			{/snippet}
		</TreeRow>

		{#if open}
			{#each nsShown(project) as ns (ns.namespace)}
				{@const nsIsOpen = nsOpen(project, ns)}
				{@const nsHref = hrefForScope({
					kind: 'namespace',
					project: project.name,
					namespace: ns.namespace,
				})}
				{@const leaves = leafWindow(
					ns.vms,
					LEAF_LIMIT,
					(v) => v.namespace === vmInView?.namespace && v.name === vmInView?.name,
				)}
				<TreeRow
					indent={1}
					active={nsScoped(project.name, ns.namespace)}
					expanded={nsIsOpen}
					ontoggle={() => setOpen(`n:${project.name}/${ns.namespace}`, !nsIsOpen)}
					href={nsHref}
					oncontextmenu={(e) =>
						ctxContainer(e, {
							project: project.name,
							repo: project.repo,
							namespace: ns.namespace,
							namespaces: [ns.namespace],
						})}
				>
					{#snippet icon()}
						<Layers size={13} class="shrink-0 text-side-dim" />
					{/snippet}
					<span class="truncate {nsIsOpen ? 'text-side-ink' : ''}">{ns.namespace}</span>
					{#snippet trailing()}
						{#if nsDrift(ns)}
							<StatusDot tone="danger" size="xs" title="A VM is out of sync" />
						{/if}
						<span class="text-xs text-side-dim">{fmt(ns.vms.length)}</span>
					{/snippet}
				</TreeRow>

				{#if nsIsOpen}
					{#each leaves.shown as vm (vm.namespace + '/' + vm.name)}
						<TreeVMRow {vm} indent={3} />
					{/each}
					{#if leaves.more > 0}
						<RailMore href="{nsHref}?tab=vms" indent={3}>{fmt(leaves.more)} more VMs</RailMore>
					{/if}
					{#if ns.vms.length === 0}
						<div class="py-1 pl-12 text-xs text-side-dim italic">no VMs</div>
					{/if}
				{/if}
			{/each}
		{/if}
	{/snippet}

	{#each folding.shown as project (project.name)}
		{@render projectNode(project)}
	{/each}

	{#if folding.folded.length > 0}
		<RailMore
			onclick={() => (unfolded = true)}
			tone={foldedTone}
			count={fmt(folding.folded.reduce((n, p) => n + vmCount(p), 0))}
		>
			{folding.folded.length} more projects
		</RailMore>
	{:else if unfolded && !filtering && !small}
		<RailMore onclick={() => (unfolded = false)}>Show the biggest only</RailMore>
	{/if}

	{#if untracked.length > 0}
		{#if folding.shown.length > 0}
			<div
				class="mt-2 border-t border-side-hover px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase"
			>
				Untracked, no repo
			</div>
		{/if}
		{#each untracked as project (project.name)}
			{@render projectNode(project)}
		{/each}
	{/if}

	<!-- Existing tenants: namespaces running VMs outside any project. Non-empty
	     only for callers the backend cleared to adopt them (namespace-create). -->
	{#if inventory.adoptable.length > 0 && !filtering}
		<div
			class="mt-2 border-t border-side-hover px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase"
		>
			Existing tenants
		</div>
		{#each inventory.adoptable as cand (cand.namespace)}
			<button
				onclick={() => (ui.modal = { kind: 'newProject', adopt: cand.namespace })}
				title="Create a project and repo for {cand.namespace} and bring its VMs under GitOps"
				class="group flex w-full items-center gap-2 rounded px-2 py-1.5 text-left hover:bg-side-hover/60"
			>
				<Layers size={13} class="shrink-0 text-side-dim" />
				<span class="min-w-0 flex-1 truncate text-side-ink">{cand.namespace}</span>
				<span class="text-xs text-side-dim">{cand.vms}</span>
				<span class="text-xs text-side-dim group-hover:text-side-ink group-hover:underline"
					>Adopt</span
				>
			</button>
		{/each}
	{/if}

	{#if filtering && folding.shown.length === 0 && untracked.length === 0}
		<div class="px-2 py-3 text-xs text-side-dim">No project or namespace matches.</div>
	{:else if projects.length === 0 && inventory.adoptable.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">
			No projects visible. Ask an admin to label a namespace with
			<code>dotvirt.io/project</code>.
		</div>
	{/if}
</div>
