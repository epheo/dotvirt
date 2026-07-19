<script lang="ts">
	import { page } from '$app/state';
	import { Folder, Layers, LayoutGrid, TriangleAlert } from 'lucide-svelte';
	import type { Project, ProjectNamespace } from '$lib/api';
	import { deriveIssues, issueCountByProject } from '$lib/issues';
	import { hrefForScope, scopeFromPath } from '$lib/nav';
	import { inventory } from '$lib/state/inventory.svelte';
	import { persisted } from '$lib/state/persisted.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ProjectSyncBadge from '$lib/components/ProjectSyncBadge.svelte';
	import StatusDot from '$lib/components/StatusDot.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Compute tree (VMs & Templates analog): All VMs → projects → namespaces
	// → VMs. Highlights derive from the URL. Projects without a repo sink below
	// the tracked ones so a broken/unadopted project never pushes real work down.
	const scope = $derived(scopeFromPath(page.url.pathname));
	const projects = $derived(inventory.inventory?.projects ?? []);
	const tracked = $derived(projects.filter((p) => !p.error));
	const untracked = $derived(projects.filter((p) => p.error));

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;

	// Collapsed state keyed by node id; default expanded, survives reloads.
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.compute', {});
	const toggle = (id: string) =>
		(collapsed.value = { ...collapsed.value, [id]: !collapsed.value[id] });
	// Untracked projects default collapsed (their warning is noise until acted
	// on); an explicit toggle is persisted and wins either way.
	const projectOpen = (p: Project) => {
		const v = collapsed.value[`p:${p.name}`];
		return v === undefined ? !p.error : !v;
	};
	const toggleProject = (p: Project) =>
		(collapsed.value = { ...collapsed.value, [`p:${p.name}`]: projectOpen(p) });

	// Drift rolls up: a namespace flags drift if any of its VMs is OutOfSync; a
	// project flags drift if any of its namespaces does.
	const nsDrift = (ns: ProjectNamespace) => ns.vms.some((v) => v.sync === 'OutOfSync');
	const projectDrift = (p: Project) => p.namespaces.some(nsDrift);
	const vmCount = (p: Project) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0);

	// Standing problems per project, for the attention badge on the row.
	const issueCounts = $derived(issueCountByProject(deriveIssues(inventory.inventory)));

	function ctxContainer(
		e: MouseEvent,
		c: { project: string; repo?: string; namespace?: string; namespaces: string[] },
	) {
		e.preventDefault();
		ui.ctx = { x: e.clientX, y: e.clientY, kind: 'container', ...c };
	}
</script>

<div class="select-none text-[13px]">
	<TreeRow
		active={scope.kind === 'all' && page.url.pathname.startsWith('/compute')}
		alignChevron
		href="/compute"
	>
		{#snippet icon()}
			<LayoutGrid size={14} class="text-ink-faint" />
		{/snippet}
		<span class="truncate font-semibold text-ink-soft">All VMs</span>
	</TreeRow>

	{#snippet projectNode(project: Project)}
		<div>
			<!-- Project: chevron toggles collapse, the label focuses the grid. -->
			<TreeRow
				active={projectScoped(project.name)}
				expanded={projectOpen(project)}
				ontoggle={() => toggleProject(project)}
				href={hrefForScope({ kind: 'project', project: project.name })}
				oncontextmenu={(e) =>
					ctxContainer(e, {
						project: project.name,
						repo: project.repo,
						namespaces: project.namespaces.map((n) => n.namespace),
					})}
			>
				{#snippet icon()}
					<Folder size={14} class="shrink-0 text-accent" />
				{/snippet}
				<span class="truncate font-semibold text-ink-soft">{project.name}</span>
				{#if project.error}
					<span class="rounded bg-warn-soft p-0.5 text-warn-ink" title={project.error}
						><TriangleAlert size={10} /></span
					>
				{:else}
					{@const n = issueCounts.get(project.name) ?? 0}
					{#if n > 0}
						<span
							class="rounded bg-warn-soft p-0.5 text-warn-ink"
							title="{n} issue{n === 1 ? '' : 's'} in this project"
							><TriangleAlert size={10} /></span
						>
					{/if}
					<!-- The Application rollup spans every kind the repo declares (segments,
					     policies, tenancy). Fall back to the VM-only drift dot only when Argo
					     isn't wired, so a project never shows two dots. -->
					<ProjectSyncBadge gitOps={project.gitOps} compact />
					{#if !project.gitOps && projectDrift(project)}
						<StatusDot tone="danger" size="xs" title="A VM is out of sync" />
					{/if}
				{/if}
				{#snippet trailing()}
					<span class="text-xs text-ink-faint">{vmCount(project)}</span>
				{/snippet}
			</TreeRow>

			{#if projectOpen(project)}
				{#if project.error}
					<div class="py-1 pr-2 pl-7 text-xs text-warn-ink italic" title={project.error}>
						{project.error}
					</div>
					{#if inventory.canManage}
						<button
							onclick={() =>
								(ui.modal = {
									kind: 'adoptProject',
									project: project.name,
									namespaces: project.namespaces.map((n) => n.namespace),
								})}
							title="Create a repo for this project and bring it under GitOps"
							class="mb-1 ml-7 rounded border border-warn/50 bg-warn-soft/60 px-2 py-0.5 text-[11px] font-medium text-warn-ink hover:bg-warn-soft"
						>
							Attach repo
						</button>
					{/if}
				{/if}

				<!-- Namespaces -->
				{#each project.namespaces as ns (ns.namespace)}
					{@const nid = `n:${project.name}/${ns.namespace}`}
					<div>
						<TreeRow
							indent={1}
							active={nsScoped(project.name, ns.namespace)}
							expanded={!collapsed.value[nid]}
							ontoggle={() => toggle(nid)}
							href={hrefForScope({
								kind: 'namespace',
								project: project.name,
								namespace: ns.namespace,
							})}
							oncontextmenu={(e) =>
								ctxContainer(e, {
									project: project.name,
									repo: project.repo,
									namespace: ns.namespace,
									namespaces: [ns.namespace],
								})}
						>
							{#snippet icon()}
								<Layers size={13} class="shrink-0 text-ink-faint" />
							{/snippet}
							<span class="truncate text-ink-soft">{ns.namespace}</span>
							{#if nsDrift(ns)}
								<StatusDot tone="danger" size="xs" title="A VM is out of sync" />
							{/if}
							{#snippet trailing()}
								<span class="text-xs text-ink-faint">{ns.vms.length}</span>
							{/snippet}
						</TreeRow>

						{#if !collapsed.value[nid]}
							{#each ns.vms as vm (vm.namespace + '/' + vm.name)}
								<TreeVMRow {vm} indent={3} />
							{/each}
							{#if ns.vms.length === 0}
								<div class="py-1 pl-12 text-xs text-ink-faint italic">no VMs</div>
							{/if}
						{/if}
					</div>
				{/each}
			{/if}
		</div>
	{/snippet}

	{#each tracked as project (project.name)}
		{@render projectNode(project)}
	{/each}

	{#if untracked.length > 0}
		{#if tracked.length > 0}
			<div
				class="mt-2 mb-0.5 border-t border-line-soft px-2 pt-1.5 text-[10px] font-semibold tracking-wide text-ink-faint uppercase"
			>
				Untracked — no repo
			</div>
		{/if}
		{#each untracked as project (project.name)}
			{@render projectNode(project)}
		{/each}
	{/if}

	{#if projects.length === 0}
		<div class="px-2 py-4 text-center text-xs text-ink-faint">
			No projects visible. Ask an admin to label a namespace with
			<code>dotvirt.io/project</code>.
		</div>
	{/if}
</div>
