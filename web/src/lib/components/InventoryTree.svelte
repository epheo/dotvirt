<script lang="ts">
	import { page } from '$app/state';
	import {
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
	import { hrefForScope, scopeFromPath, sectionOf, vmHref } from '$lib/nav';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import TreeRow from './TreeRow.svelte';

	let {
		inventory,
		staged,
		networks = [],
		canManage = false,
		oncontextvm,
		oncontextcontainer,
		onattachrepo
	}: {
		inventory: Inventory;
		staged: Map<string, DraftItem>;
		networks?: PortGroup[]; // port-group catalog, for friendly Segments-lens grouping
		canManage?: boolean; // gates the platform-tier "Attach repo" CTA on repoless projects
		oncontextvm?: (vm: VM, x: number, y: number) => void;
		oncontextcontainer?: (
			c: { project: string; repo?: string; namespace?: string; namespaces: string[] },
			x: number,
			y: number
		) => void;
		onattachrepo?: (project: string, namespaces: string[]) => void;
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

	// Everything the tree highlights derives from the URL: the lens from the
	// section, the scoped row from the path, the selected VM from the /vm route.
	const path = $derived(page.url.pathname);
	const scope = $derived<Scope>(scopeFromPath(path));
	const section = $derived(sectionOf(path));

	// Inventory lens — vCenter's four organizing views over one object set:
	// Projects (VMs & Templates), Nodes (Hosts & Clusters), Networks, Storage.
	// Each lens is a section root; the /vm and /catalog routes keep the Projects tree.
	type Lens = 'project' | 'node' | 'network' | 'storage';
	const lens = $derived<Lens>(
		section === 'hosts'
			? 'node'
			: section === 'networking'
				? 'network'
				: section === 'storage'
					? 'storage'
					: 'project'
	);
	const LENSES: { id: Lens; label: string; href: string }[] = [
		{ id: 'project', label: 'Projects', href: '/compute' },
		{ id: 'node', label: 'Nodes', href: '/hosts' },
		{ id: 'network', label: 'Segments', href: '/networking' },
		{ id: 'storage', label: 'Storage', href: '/storage' }
	];

	const projectScoped = (name: string) => scope.kind === 'project' && scope.project === name;
	const nsScoped = (project: string, ns: string) =>
		scope.kind === 'namespace' && scope.project === project && scope.namespace === ns;

	// Collapsed state keyed by node id; default expanded.
	let collapsed = $state<Record<string, boolean>>({});
	const toggle = (id: string) => (collapsed[id] = !collapsed[id]);

	const selectedKey = $derived.by(() => {
		const parts = path.split('/');
		return parts[1] === 'vm' && parts.length >= 4
			? `${decodeURIComponent(parts[2])}/${decodeURIComponent(parts[3])}`
			: '';
	});
	const isSelected = (vm: VM) => selectedKey === `${vm.namespace}/${vm.name}`;

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

{#snippet vmRow(vm: VM, indent: 2 | 3)}
	{@const sc = staged.get(vm.namespace + '/' + vm.name)}
	<TreeRow
		{indent}
		active={isSelected(vm)}
		href={vmHref(vm.namespace, vm.name)}
		oncontextmenu={(e) => ctxVM(e, vm)}
	>
		{#snippet icon()}
			<PowerDot power={vm.power} paused={vm.paused} />
		{/snippet}
		<span class="truncate {sc?.kind === 'delete' ? 'text-ink-faint line-through' : 'text-ink-soft'}"
			>{vm.name}</span
		>
		{#snippet trailing()}
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
		{/snippet}
	</TreeRow>
{/snippet}

<div class="select-none text-[13px]">
	<!-- Catalog: a pinned destination (cluster images, instance types, networks,
	     storage classes) — vCenter parks its Content Libraries in the left nav, above
	     the inventory lenses. -->
	<TreeRow
		active={section === 'catalog'}
		alignChevron
		border
		href="/catalog"
		title="Browse the cluster's images, instance types, preferences, networks and storage classes"
	>
		{#snippet icon()}
			<Library size={14} class="text-ink-faint" />
		{/snippet}
		<span class="truncate font-semibold text-ink-soft">Catalog</span>
	</TreeRow>

	<!-- Topology: the network map (Tier-0 → Tier-1 → Segment → VM) — the
	     Networking section's home. -->
	<TreeRow
		active={path === '/networking'}
		alignChevron
		border
		href="/networking"
		title="Network Topology — the Tier-0 → Tier-1 → Segment → VM map"
	>
		{#snippet icon()}
			<Workflow size={14} class="text-ink-faint" />
		{/snippet}
		<span class="truncate font-semibold text-ink-soft">Topology</span>
	</TreeRow>

	<!-- Lens switch (one object set, four organizing views). -->
	<div class="flex flex-wrap gap-1 border-b border-line px-2 py-1.5">
		{#each LENSES as l (l.id)}
			<a
				href={l.href}
				class="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs {lens === l.id
					? 'bg-select font-medium text-accent-ink'
					: 'text-ink-muted hover:bg-inset'}"
			>
				{#if l.id === 'project'}<Folder size={12} />{:else if l.id === 'node'}<Server
						size={12}
					/>{:else if l.id === 'network'}<Network size={12} />{:else}<Database size={12} />{/if}
				{l.label}
			</a>
		{/each}
	</div>

	<!-- All VMs: the whole-inventory grid. -->
	<TreeRow
		active={scope.kind === 'all' && section !== 'catalog' && path !== '/networking'}
		alignChevron
		href="/compute"
	>
		{#snippet icon()}
			<LayoutGrid size={14} class="text-ink-faint" />
		{/snippet}
		<span class="truncate font-semibold text-ink-soft">All VMs</span>
	</TreeRow>

	{#if lens === 'project'}
		{#each inventory.projects as project (project.name)}
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
									<span class="h-1.5 w-1.5 rounded-full bg-red-500" title="A VM is OutOfSync"
									></span>
								{/if}
								{#snippet trailing()}
									<span class="text-xs text-ink-faint">{ns.vms.length}</span>
								{/snippet}
							</TreeRow>

							{#if !collapsed[nid]}
								{#each ns.vms as vm (vm.namespace + '/' + vm.name)}
									{@render vmRow(vm, 3)}
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

		{#if inventory.projects.length === 0}
			<div class="px-2 py-4 text-center text-xs text-ink-faint">
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
				<TreeRow
					active={flatScoped(key)}
					expanded={!collapsed[gid]}
					ontoggle={() => toggle(gid)}
					href={hrefForScope(flatScope(key))}
				>
					{#snippet icon()}
						{#if lens === 'node'}<Server size={14} class="shrink-0 text-ink-muted" />
						{:else if lens === 'network'}<Network size={14} class="shrink-0 text-ink-muted" />
						{:else}<Database size={14} class="shrink-0 text-ink-muted" />{/if}
					{/snippet}
					<span class="truncate font-semibold text-ink-soft">{key}</span>
					{#snippet trailing()}
						<span class="text-xs text-ink-faint">{vms.length}</span>
					{/snippet}
				</TreeRow>
				{#if !collapsed[gid]}
					{#each vms as vm (vm.namespace + '/' + vm.name)}
						{@render vmRow(vm, 2)}
					{/each}
				{/if}
			</div>
		{/each}

		{#if flatGroups.length === 0}
			<div class="px-2 py-4 text-center text-xs text-ink-faint">No VMs in this view.</div>
		{/if}
	{/if}
</div>
