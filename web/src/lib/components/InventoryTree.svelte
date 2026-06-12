<script lang="ts">
	import { ChevronDown, ChevronRight, Folder, Layers, LayoutGrid } from 'lucide-svelte';
	import type { Inventory, Project, ProjectNamespace, VM } from '$lib/api';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	type Scope =
		| { kind: 'all' }
		| { kind: 'project'; project: string }
		| { kind: 'namespace'; project: string; namespace: string };

	let {
		inventory,
		selected,
		scope,
		onselect,
		onscope
	}: {
		inventory: Inventory;
		selected: VM | null;
		scope: Scope;
		onselect: (vm: VM) => void;
		onscope: (s: Scope) => void;
	} = $props();

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;

	// Collapsed state keyed by node id; default expanded.
	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	const isSelected = (vm: VM) => selected?.namespace === vm.namespace && selected?.name === vm.name;

	// Drift rolls up: a namespace flags drift if any of its VMs is OutOfSync; a
	// project flags drift if any of its namespaces does.
	const nsDrift = (ns: ProjectNamespace) => ns.vms.some((v) => v.sync === 'OutOfSync');
	const projectDrift = (p: Project) => p.namespaces.some(nsDrift);
	const vmCount = (p: Project) => p.namespaces.reduce((n, ns) => n + ns.vms.length, 0);
</script>

<div class="select-none text-[13px]">
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

	{#each inventory.projects as project (project.name)}
		{@const pid = `p:${project.name}`}
		<div>
			<!-- Project: chevron toggles collapse, the label sets the grid scope. -->
			<div
				class="flex w-full items-center gap-1 px-2 py-1 hover:bg-slate-100
					{projectScoped(project.name) ? 'bg-blue-50' : ''}"
			>
				<button class="flex w-3 items-center text-slate-400" onclick={() => toggle(pid)} title="Expand/collapse">
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
							<button class="flex w-3 items-center text-slate-400" onclick={() => toggle(nid)} title="Expand/collapse">
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
							{#each ns.vms as vm (vm.name)}
								<button
									class="flex w-full items-center gap-2 py-1 pr-2 pl-12 text-left hover:bg-blue-50
										{isSelected(vm) ? 'bg-blue-100 hover:bg-blue-100' : ''}"
									onclick={() => onselect(vm)}
								>
									<PowerDot power={vm.power} paused={vm.paused} />
									<span class="truncate text-slate-700">{vm.name}</span>
									<span class="ml-auto"><SyncBadge sync={vm.sync} compact /></span>
								</button>
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
</div>
