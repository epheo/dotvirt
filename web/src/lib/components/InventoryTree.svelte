<script lang="ts">
	import {
		ChevronDown,
		ChevronRight,
		Database,
		Folder,
		Layers,
		LayoutGrid,
		Library,
		Network,
		Pencil,
		Server,
		Trash2,
		Workflow
	} from 'lucide-svelte';
	import type {
		DraftItem,
		Inventory,
		Network as PortGroup,
		Project,
		ProjectNamespace,
		VM
	} from '$lib/api';
	import { vmNetworkKeys, vmStorageKeys, type Scope } from '$lib/lenses';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let {
		inventory,
		selected,
		scope,
		staged,
		networks = [],
		canManage = false,
		catalogActive = false,
		topologyActive = false,
		onselect,
		onscope,
		oncontextvm,
		oncontextcontainer,
		onattachrepo,
		oncatalog,
		ontopology
	}: {
		inventory: Inventory;
		selected: VM | null;
		scope: Scope;
		staged: Map<string, DraftItem>;
		networks?: PortGroup[]; // port-group catalog, for friendly Segments-lens grouping
		canManage?: boolean; // gates the platform-tier "Attach repo" CTA on repoless projects
		catalogActive?: boolean; // highlights the pinned Catalog entry while its panel is open
		topologyActive?: boolean; // highlights the pinned Topology entry while the map is open
		onselect: (vm: VM) => void;
		onscope: (s: Scope) => void;
		oncontextvm?: (vm: VM, x: number, y: number) => void;
		oncontextcontainer?: (
			c: { project: string; repo?: string; namespace?: string; namespaces: string[] },
			x: number,
			y: number
		) => void;
		onattachrepo?: (project: string, namespaces: string[]) => void;
		oncatalog?: () => void; // opens the cluster resource catalog (a destination, not a scope)
		ontopology?: () => void; // opens the network topology map (a destination, not a scope)
	} = $props();

	function ctxVM(e: MouseEvent, vm: VM) {
		if (!oncontextvm) return;
		e.preventDefault();
		oncontextvm(vm, e.clientX, e.clientY);
	}
	function ctxContainer(
		e: MouseEvent,
		c: { project: string; repo?: string; namespace?: string; namespaces: string[] }
	) {
		if (!oncontextcontainer) return;
		e.preventDefault();
		oncontextcontainer(c, e.clientX, e.clientY);
	}

	// Inventory lens — vCenter's four organizing views over one object set:
	// Projects (VMs & Templates), Nodes (Hosts & Clusters), Networks, Storage.
	// Switching resets the grid scope.
	type Lens = 'project' | 'node' | 'network' | 'storage';
	let lens = $state<Lens>('project');
	function setLens(l: Lens) {
		if (l === lens) return;
		lens = l;
		onscope({ kind: 'all' });
	}
	const LENSES: { id: Lens; label: string }[] = [
		{ id: 'project', label: 'Projects' },
		{ id: 'node', label: 'Nodes' },
		{ id: 'network', label: 'Segments' },
		{ id: 'storage', label: 'Storage' }
	];

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

	// Flat groupings for the non-project lenses: one group per key, a VM under
	// every key it matches (a VM with two NICs shows under both networks, as
	// vCenter does). Keys come from $lib/lenses so the grid filter agrees.
	const flatGroups = $derived.by(() => {
		if (lens === 'project') return [];
		const m = new Map<string, VM[]>();
		const add = (key: string, vm: VM) => {
			if (!m.has(key)) m.set(key, []);
			m.get(key)!.push(vm);
		};
		for (const p of inventory.projects)
			for (const ns of p.namespaces)
				for (const vm of ns.vms) {
					if (lens === 'node') add(vm.nodeName || '(unscheduled)', vm);
					else if (lens === 'network') for (const k of vmNetworkKeys(vm, networks)) add(k, vm);
					else for (const k of vmStorageKeys(vm)) add(k, vm);
				}
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});

	// Scope target + highlight for a flat-lens group row.
	const flatScope = (key: string): Scope =>
		lens === 'node'
			? { kind: 'node', node: key }
			: lens === 'network'
				? { kind: 'network', network: key }
				: { kind: 'storage', storageClass: key };
	const flatScoped = (key: string) =>
		(scope.kind === 'node' && scope.node === key) ||
		(scope.kind === 'network' && scope.network === key) ||
		(scope.kind === 'storage' && scope.storageClass === key);
</script>

{#snippet vmRow(vm: VM, pad: string)}
	{@const sc = staged.get(vm.namespace + '/' + vm.name)}
	<button
		class="flex w-full items-center gap-2 py-1 pr-2 text-left hover:bg-blue-50 {pad}
			{isSelected(vm) ? 'bg-blue-100 hover:bg-blue-100' : ''}"
		onclick={() => onselect(vm)}
		oncontextmenu={(e) => ctxVM(e, vm)}
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
				<SyncBadge sync={vm.sync} error={vm.syncError} compact />
			{/if}
		</span>
	</button>
{/snippet}

<div class="select-none text-[13px]">
	<!-- Catalog: a pinned destination (cluster images, instance types, networks,
	     storage classes) — vCenter parks its Content Libraries in the left nav, above
	     the inventory lenses. Opens the catalog panel rather than re-scoping the grid. -->
	{#if oncatalog}
		<button
			class="flex w-full items-center gap-1 border-b border-slate-200 px-2 py-1.5 text-left hover:bg-slate-100
				{catalogActive ? 'bg-blue-50' : ''}"
			onclick={oncatalog}
			title="Browse the cluster's images, instance types, preferences, networks and storage classes"
		>
			<span class="w-3"></span>
			<Library size={14} class="text-slate-400" />
			<span class="font-semibold text-slate-700">Catalog</span>
		</button>
	{/if}

	<!-- Topology: the network map (Tier-0 → Tier-1 → Segment → VM), a destination like
	     Catalog rather than a scope. -->
	{#if ontopology}
		<button
			class="flex w-full items-center gap-1 border-b border-slate-200 px-2 py-1.5 text-left hover:bg-slate-100
				{topologyActive ? 'bg-blue-50' : ''}"
			onclick={ontopology}
			title="Network Topology — the Tier-0 → Tier-1 → Segment → VM map"
		>
			<span class="w-3"></span>
			<Workflow size={14} class="text-slate-400" />
			<span class="font-semibold text-slate-700">Topology</span>
		</button>
	{/if}

	<!-- Lens switch (one object set, four organizing views). -->
	<div class="flex flex-wrap gap-1 border-b border-slate-200 px-2 py-1.5">
		{#each LENSES as l (l.id)}
			<button
				class="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs {lens === l.id
					? 'bg-blue-100 font-medium text-blue-700'
					: 'text-slate-500 hover:bg-slate-100'}"
				onclick={() => setLens(l.id)}
			>
				{#if l.id === 'project'}<Folder size={12} />{:else if l.id === 'node'}<Server
						size={12}
					/>{:else if l.id === 'network'}<Network size={12} />{:else}<Database size={12} />{/if}
				{l.label}
			</button>
		{/each}
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
						oncontextmenu={(e) =>
							ctxContainer(e, {
								project: project.name,
								repo: project.repo,
								namespaces: project.namespaces.map((n) => n.namespace)
							})}
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
						{#if canManage && onattachrepo}
							<button
								onclick={() =>
									onattachrepo?.(
										project.name,
										project.namespaces.map((n) => n.namespace)
									)}
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
							<div
								class="flex w-full items-center gap-1 py-1 pr-2 pl-5 hover:bg-slate-100
									{nsScoped(project.name, ns.namespace) ? 'bg-blue-50' : ''}"
							>
								<button
									class="flex w-3 items-center text-slate-400"
									onclick={() => toggle(nid)}
									title="Expand/collapse"
								>
									{#if collapsed[nid]}<ChevronRight size={12} />{:else}<ChevronDown
											size={12}
										/>{/if}
								</button>
								<button
									class="flex min-w-0 flex-1 items-center gap-1 text-left"
									onclick={() =>
										onscope({ kind: 'namespace', project: project.name, namespace: ns.namespace })}
									oncontextmenu={(e) =>
										ctxContainer(e, {
											project: project.name,
											repo: project.repo,
											namespace: ns.namespace,
											namespaces: [ns.namespace]
										})}
								>
									<Layers size={13} class="shrink-0 text-slate-400" />
									<span class="truncate text-slate-600">{ns.namespace}</span>
									{#if nsDrift(ns)}
										<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"
										></span>
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
		<!-- Flat lenses: Nodes (physical placement, the Hosts & Clusters analog),
		     Networks (by NIC network), Storage (by dataVolume storage class). -->
		{#each flatGroups as [key, vms] (key)}
			{@const gid = `${lens}:${key}`}
			<div>
				<div
					class="flex w-full items-center gap-1 px-2 py-1 hover:bg-slate-100
						{flatScoped(key) ? 'bg-blue-50' : ''}"
				>
					<button
						class="flex w-3 items-center text-slate-400"
						onclick={() => toggle(gid)}
						title="Expand/collapse"
					>
						{#if collapsed[gid]}<ChevronRight size={12} />{:else}<ChevronDown size={12} />{/if}
					</button>
					<button
						class="flex min-w-0 flex-1 items-center gap-1 text-left"
						onclick={() => onscope(flatScope(key))}
					>
						{#if lens === 'node'}<Server size={14} class="shrink-0 text-slate-500" />
						{:else if lens === 'network'}<Network size={14} class="shrink-0 text-slate-500" />
						{:else}<Database size={14} class="shrink-0 text-slate-500" />{/if}
						<span class="truncate font-semibold text-slate-700">{key}</span>
						<span class="ml-auto text-xs text-slate-400">{vms.length}</span>
					</button>
				</div>
				{#if !collapsed[gid]}
					{#each vms as vm (vm.namespace + '/' + vm.name)}
						{@render vmRow(vm, 'pl-7')}
					{/each}
				{/if}
			</div>
		{/each}

		{#if flatGroups.length === 0}
			<div class="px-2 py-4 text-center text-xs text-slate-400">No VMs in this view.</div>
		{/if}
	{/if}
</div>
