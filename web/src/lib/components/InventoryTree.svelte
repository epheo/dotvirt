<script lang="ts">
	import type { Inventory, Project, ProjectNamespace, VM } from '$lib/api';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let {
		inventory,
		selected,
		onselect
	}: {
		inventory: Inventory;
		selected: VM | null;
		onselect: (vm: VM) => void;
	} = $props();

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
	{#each inventory.projects as project (project.name)}
		{@const pid = `p:${project.name}`}
		<div>
			<!-- Project -->
			<button
				class="flex w-full items-center gap-1 px-2 py-1 text-left hover:bg-slate-100"
				onclick={() => toggle(pid)}
			>
				<span class="w-3 text-slate-400">{collapsed[pid] ? '▸' : '▾'}</span>
				<span class="text-blue-500">▦</span>
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
						<button
							class="flex w-full items-center gap-1 py-1 pr-2 pl-5 text-left hover:bg-slate-100"
							onclick={() => toggle(nid)}
						>
							<span class="w-3 text-slate-400">{collapsed[nid] ? '▸' : '▾'}</span>
							<span class="text-slate-400">▣</span>
							<span class="truncate text-slate-600">{ns.namespace}</span>
							{#if nsDrift(ns)}
								<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"></span>
							{/if}
							<span class="ml-auto text-xs text-slate-400">{ns.vms.length}</span>
						</button>

						{#if !collapsed[nid]}
							{#each ns.vms as vm (vm.name)}
								<button
									class="flex w-full items-center gap-2 py-1 pr-2 pl-12 text-left hover:bg-blue-50
										{isSelected(vm) ? 'bg-blue-100 hover:bg-blue-100' : ''}"
									onclick={() => onselect(vm)}
								>
									<PowerDot power={vm.power} />
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
