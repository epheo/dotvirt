<script lang="ts">
	import type { Inventory, VM } from '$lib/api';
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

	// Track collapsed namespaces by name; default expanded.
	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (ns: string) => (collapsed[ns] = !collapsed[ns]);

	const isSelected = (vm: VM) =>
		selected?.namespace === vm.namespace && selected?.name === vm.name;

	// Roll drift up to the project node: a namespace flags drift if any VM in it
	// is OutOfSync (vCenter-style folder badge).
	const projectDrift = (vms: VM[]) => vms.some((v) => v.sync === 'OutOfSync');
</script>

<div class="select-none text-[13px]">
	{#each inventory.projects as project (project.namespace)}
		<div>
			<button
				class="flex w-full items-center gap-1 px-2 py-1 text-left hover:bg-slate-100"
				onclick={() => toggle(project.namespace)}
			>
				<span class="w-3 text-slate-400">{collapsed[project.namespace] ? '▸' : '▾'}</span>
				<span class="text-slate-500">▣</span>
				<span class="font-medium text-slate-700">{project.namespace}</span>
				{#if projectDrift(project.vms)}
					<span
						class="h-1.5 w-1.5 rounded-full bg-red-500"
						title="One or more VMs are OutOfSync"
					></span>
				{/if}
				<span class="ml-auto text-xs text-slate-400">{project.vms.length}</span>
			</button>

			{#if !collapsed[project.namespace]}
				{#each project.vms as vm (vm.name)}
					<button
						class="flex w-full items-center gap-2 py-1 pr-2 pl-8 text-left hover:bg-blue-50
							{isSelected(vm) ? 'bg-blue-100 hover:bg-blue-100' : ''}"
						onclick={() => onselect(vm)}
					>
						<PowerDot power={vm.power} />
						<span class="truncate text-slate-700">{vm.name}</span>
						<span class="ml-auto"><SyncBadge sync={vm.sync} compact /></span>
					</button>
				{/each}
				{#if project.vms.length === 0}
					<div class="py-1 pl-8 text-xs text-slate-400 italic">no VMs</div>
				{/if}
			{/if}
		</div>
	{/each}

	{#if inventory.projects.length === 0}
		<div class="px-2 py-4 text-center text-xs text-slate-400">No projects on this branch</div>
	{/if}
</div>
