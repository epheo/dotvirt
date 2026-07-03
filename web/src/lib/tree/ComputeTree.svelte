<script lang="ts">
	import { page } from '$app/state';
	import { Folder, Layers, LayoutGrid } from 'lucide-svelte';
	import type { Project, ProjectNamespace } from '$lib/api';
	import { hrefForScope, scopeFromPath } from '$lib/nav';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';
	import TreeVMRow from './TreeVMRow.svelte';

	// The Compute tree (VMs & Templates analog): All VMs → projects → namespaces
	// → VMs. Highlights derive from the URL.
	const scope = $derived(scopeFromPath(page.url.pathname));
	const projects = $derived(inventory.inventory?.projects ?? []);

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;

	// Collapsed state keyed by node id; default expanded.
	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	// Drift rolls up: a namespace flags drift if any of its VMs is OutOfSync; a
	// project flags drift if any of its namespaces does.
	const nsDrift = (ns: ProjectNamespace) => ns.vms.some((v) => v.sync === 'OutOfSync');
	const projectDrift = (p: Project) => p.namespaces.some(nsDrift);
	const vmCount = (p: Project) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0);

	function ctxContainer(
		e: MouseEvent,
		c: { project: string; repo?: string; namespace?: string; namespaces: string[] }
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

	{#each projects as project (project.name)}
		{@const pid = `p:${project.name}`}
		<div>
			<!-- Project: chevron toggles collapse, the label focuses the grid. -->
			<TreeRow
				active={projectScoped(project.name)}
				expanded={!collapsed[pid]}
				ontoggle={() => toggle(pid)}
				href={hrefForScope({ kind: 'project', project: project.name })}
				oncontextmenu={(e) =>
					ctxContainer(e, {
						project: project.name,
						repo: project.repo,
						namespaces: project.namespaces.map((n) => n.namespace)
					})}
			>
				{#snippet icon()}
					<Folder size={14} class="shrink-0 text-blue-500" />
				{/snippet}
				<span class="truncate font-semibold text-ink-soft">{project.name}</span>
				{#if project.error}
					<span
						class="rounded bg-amber-100 px-1 text-[10px] font-medium text-amber-700"
						title={project.error}>!</span
					>
				{:else if projectDrift(project)}
					<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"></span>
				{/if}
				{#snippet trailing()}
					<span class="text-xs text-ink-faint">{vmCount(project)}</span>
				{/snippet}
			</TreeRow>

			{#if !collapsed[pid]}
				{#if project.error}
					<div class="py-1 pr-2 pl-7 text-xs text-amber-600 italic" title={project.error}>
						{project.error}
					</div>
					{#if inventory.canManage}
						<button
							onclick={() =>
								(ui.modal = {
									kind: 'adoptProject',
									project: project.name,
									namespaces: project.namespaces.map((n) => n.namespace)
								})}
							title="Create a repo for this project and bring it under GitOps"
							class="mb-1 ml-7 rounded border border-amber-300 bg-amber-50 px-2 py-0.5 text-[11px] font-medium text-amber-700 hover:bg-amber-100"
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
							expanded={!collapsed[nid]}
							ontoggle={() => toggle(nid)}
							href={hrefForScope({
								kind: 'namespace',
								project: project.name,
								namespace: ns.namespace
							})}
							oncontextmenu={(e) =>
								ctxContainer(e, {
									project: project.name,
									repo: project.repo,
									namespace: ns.namespace,
									namespaces: [ns.namespace]
								})}
						>
							{#snippet icon()}
								<Layers size={13} class="shrink-0 text-ink-faint" />
							{/snippet}
							<span class="truncate text-slate-600">{ns.namespace}</span>
							{#if nsDrift(ns)}
								<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"></span>
							{/if}
							{#snippet trailing()}
								<span class="text-xs text-ink-faint">{ns.vms.length}</span>
							{/snippet}
						</TreeRow>

						{#if !collapsed[nid]}
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
	{/each}

	{#if projects.length === 0}
		<div class="px-2 py-4 text-center text-xs text-ink-faint">
			No projects visible. Ask an admin to label a namespace with
			<code>dotvirt.io/project</code>.
		</div>
	{/if}
</div>
