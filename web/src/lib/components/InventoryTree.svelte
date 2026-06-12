<script lang="ts">
	import {
		ChevronDown,
		ChevronRight,
		Folder,
		Layers,
		LayoutGrid,
		Pencil,
		Server,
		Trash2
	} from 'lucide-svelte';
	import type { DraftItem, Inventory, Project, ProjectNamespace, VM } from '$lib/api';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	type Scope =
		| { kind: 'all' }
		| { kind: 'project'; project: string }
		| { kind: 'namespace'; project: string; namespace: string }
		| { kind: 'node'; node: string };

	let {
		inventory,
		selected,
		scope,
		staged,
		onselect,
		onscope
	}: {
		inventory: Inventory;
		selected: VM | null;
		scope: Scope;
		staged: Map<string, DraftItem>;
		onselect: (vm: VM) => void;
		onscope: (s: Scope) => void;
	} = $props();

	// Inventory lens: by Project (tenant/logical, like vCenter's VMs & Templates) or
	// by Node (physical, like Hosts & Clusters). Switching resets the grid scope.
	let lens = $state<'project' | 'node'>('project');
	function setLens(l: 'project' | 'node') {
		if (l === lens) return;
		lens = l;
		onscope({ kind: 'all' });
	}

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;
	const nodeScoped = (node: string) => scope.kind === 'node' && scope.node === node;

	// Collapsed state keyed by node id; default expanded.
	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	const isSelected = (vm: VM) => selected?.namespace === vm.namespace && selected?.name === vm.name;

	// Drift rolls up: a namespace flags drift if any of its VMs is OutOfSync; a
	// project flags drift if any of its namespaces does.
	const nsDrift = (ns: ProjectNamespace) => ns.vms.some((v) => v.sync === 'OutOfSync');
	const projectDrift = (p: Project) => p.namespaces.some(nsDrift);
	const vmCount = (p: Project) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0);

	// VMs grouped by the node they run on, for the "By Node" lens.
	const byNode = $derived.by(() => {
		const m = new Map<string, VM[]>();
		for (const p of inventory.projects)
			for (const ns of p.namespaces)
				for (const vm of ns.vms) {
					const node = vm.nodeName || '(unscheduled)';
					if (!m.has(node)) m.set(node, []);
					m.get(node)!.push(vm);
				}
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});
</script>

{#snippet vmRow(vm: VM, pad: string)}
	{@const sc = staged.get(vm.namespace + '/' + vm.name)}
	<button
		class="flex w-full items-center gap-2 py-1 pr-2 text-left hover:bg-blue-50 {pad}
			{isSelected(vm) ? 'bg-blue-100 hover:bg-blue-100' : ''}"
		onclick={() => onselect(vm)}
	>
		<PowerDot power={vm.power} paused={vm.paused} />
		<span
			class="truncate {sc?.kind === 'delete' ? 'text-slate-400 line-through' : 'text-slate-700'}"
			>{vm.name}</span
		>
		<span class="ml-auto">
			{#if sc}
				<span
					class="inline-flex items-center rounded px-1 text-[10px] font-medium {sc.kind === 'delete'
						? 'bg-red-100 text-red-700'
						: 'bg-blue-100 text-blue-700'}"
					title="Staged {sc.kind}"
				>
					{#if sc.kind === 'delete'}<Trash2 size={10} />{:else}<Pencil size={10} />{/if}
				</span>
			{:else}
				<SyncBadge sync={vm.sync} compact />
			{/if}
		</span>
	</button>
{/snippet}

<div class="select-none text-[13px]">
	<!-- Lens switch (one object set, two organizing views). -->
	<div class="flex gap-1 border-b border-slate-200 px-2 py-1.5">
		<button
			class="flex items-center gap-1 rounded px-2 py-0.5 text-xs {lens === 'project'
				? 'bg-blue-100 font-medium text-blue-700'
				: 'text-slate-500 hover:bg-slate-100'}"
			onclick={() => setLens('project')}
		>
			<Folder size={12} /> Projects
		</button>
		<button
			class="flex items-center gap-1 rounded px-2 py-0.5 text-xs {lens === 'node'
				? 'bg-blue-100 font-medium text-blue-700'
				: 'text-slate-500 hover:bg-slate-100'}"
			onclick={() => setLens('node')}
		>
			<Server size={12} /> Nodes
		</button>
	</div>

	<!-- All VMs: resets the grid scope to the whole inventory. -->
	<button
		class="flex w-full items-center gap-1 px-2 py-1 text-left hover:bg-slate-100
			{scope.kind === 'all' ? 'bg-blue-50' : ''}"
		onclick={() => onscope({ kind: 'all' })}
	>
		<span class="w-3"></span>
		<LayoutGrid size={14} class="text-slate-400" />
		<span class="font-semibold text-slate-700">All VMs</span>
	</button>

	{#if lens === 'project'}
		{#each inventory.projects as project (project.name)}
			{@const pid = `p:${project.name}`}
			<div>
				<!-- Project: chevron toggles collapse, the label sets the grid scope. -->
				<div
					class="flex w-full items-center gap-1 px-2 py-1 hover:bg-slate-100
						{projectScoped(project.name) ? 'bg-blue-50' : ''}"
				>
					<button
						class="flex w-3 items-center text-slate-400"
						onclick={() => toggle(pid)}
						title="Expand/collapse"
					>
						{#if collapsed[pid]}<ChevronRight size={12} />{:else}<ChevronDown size={12} />{/if}
					</button>
					<button
						class="flex min-w-0 flex-1 items-center gap-1 text-left"
						onclick={() => onscope({ kind: 'project', project: project.name })}
					>
						<Folder size={14} class="shrink-0 text-blue-500" />
						<span class="truncate font-semibold text-slate-700">{project.name}</span>
						{#if project.error}
							<span
								class="rounded bg-amber-100 px-1 text-[10px] font-medium text-amber-700"
								title={project.error}>!</span
							>
						{:else if projectDrift(project)}
							<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"></span>
						{/if}
						<span class="ml-auto text-xs text-slate-400">{vmCount(project)}</span>
					</button>
				</div>

				{#if !collapsed[pid]}
					{#if project.error}
						<div class="py-1 pr-2 pl-7 text-xs text-amber-600 italic" title={project.error}>
							{project.error}
						</div>
					{/if}

					<!-- Namespaces -->
					{#each project.namespaces as ns (ns.namespace)}
						{@const nid = `n:${project.name}/${ns.namespace}`}
						<div>
							<div
								class="flex w-full items-center gap-1 py-1 pr-2 pl-5 hover:bg-slate-100
									{nsScoped(project.name, ns.namespace) ? 'bg-blue-50' : ''}"
							>
								<button
									class="flex w-3 items-center text-slate-400"
									onclick={() => toggle(nid)}
									title="Expand/collapse"
								>
									{#if collapsed[nid]}<ChevronRight size={12} />{:else}<ChevronDown size={12} />{/if}
								</button>
								<button
									class="flex min-w-0 flex-1 items-center gap-1 text-left"
									onclick={() =>
										onscope({ kind: 'namespace', project: project.name, namespace: ns.namespace })}
								>
									<Layers size={13} class="shrink-0 text-slate-400" />
									<span class="truncate text-slate-600">{ns.namespace}</span>
									{#if nsDrift(ns)}
										<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"></span>
									{/if}
									<span class="ml-auto text-xs text-slate-400">{ns.vms.length}</span>
								</button>
							</div>

							{#if !collapsed[nid]}
								{#each ns.vms as vm (vm.namespace + '/' + vm.name)}
									{@render vmRow(vm, 'pl-12')}
								{/each}
								{#if ns.vms.length === 0}
									<div class="py-1 pl-12 text-xs text-slate-400 italic">no VMs</div>
								{/if}
							{/if}
						</div>
					{/each}
				{/if}
			</div>
		{/each}

		{#if inventory.projects.length === 0}
			<div class="px-2 py-4 text-center text-xs text-slate-400">
				No projects visible. Ask an admin to label a namespace with
				<code>dotvirt.io/project</code>.
			</div>
		{/if}
	{:else}
		<!-- By Node: physical placement (the Hosts & Clusters analog). -->
		{#each byNode as [node, vms] (node)}
			{@const nid = `node:${node}`}
			<div>
				<div
					class="flex w-full items-center gap-1 px-2 py-1 hover:bg-slate-100
						{nodeScoped(node) ? 'bg-blue-50' : ''}"
				>
					<button
						class="flex w-3 items-center text-slate-400"
						onclick={() => toggle(nid)}
						title="Expand/collapse"
					>
						{#if collapsed[nid]}<ChevronRight size={12} />{:else}<ChevronDown size={12} />{/if}
					</button>
					<button
						class="flex min-w-0 flex-1 items-center gap-1 text-left"
						onclick={() => onscope({ kind: 'node', node })}
					>
						<Server size={14} class="shrink-0 text-slate-500" />
						<span class="truncate font-semibold text-slate-700">{node}</span>
						<span class="ml-auto text-xs text-slate-400">{vms.length}</span>
					</button>
				</div>
				{#if !collapsed[nid]}
					{#each vms as vm (vm.namespace + '/' + vm.name)}
						{@render vmRow(vm, 'pl-7')}
					{/each}
				{/if}
			</div>
		{/each}

		{#if byNode.length === 0}
			<div class="px-2 py-4 text-center text-xs text-slate-400">No running VMs to place.</div>
		{/if}
	{/if}
</div>
