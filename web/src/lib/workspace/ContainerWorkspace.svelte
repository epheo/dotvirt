<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, Unauthorized, type VM } from '$lib/api';
	import { vmNetworkKeys, vmStorageKeys, type Scope } from '$lib/lenses';
	import { vmHref } from '$lib/nav';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import BulkActionsBar from '$lib/components/BulkActionsBar.svelte';
	import ClusterSummary from '$lib/components/ClusterSummary.svelte';
	import ConfirmDelete from '$lib/components/ConfirmDelete.svelte';
	import ContainerConfigure from '$lib/components/ContainerConfigure.svelte';
	import ContainerMonitor from '$lib/components/ContainerMonitor.svelte';
	import ContextMenu from '$lib/components/ContextMenu.svelte';
	import MenuItem from '$lib/components/MenuItem.svelte';
	import PendingBanner from '$lib/components/PendingBanner.svelte';
	import Permissions from '$lib/components/Permissions.svelte';
	import TabBar from '$lib/components/TabBar.svelte';
	import VMTable from '$lib/components/VMTable.svelte';
	import NodeConfigure from './NodeConfigure.svelte';
	import SegmentSummary from './SegmentSummary.svelte';
	import StorageClassSummary from './StorageClassSummary.svelte';

	// The container workspace: every inventory level gets the same breadcrumb +
	// tab chrome — vCenter's "same tabs at every level". The tab SET follows the
	// object kind: compute containers carry the full set, a host drops
	// Permissions (nodes aren't namespaced), segments and storage classes are
	// fact sheets (Summary + their VMs).
	let {
		scope,
		trail
	}: {
		scope: Scope;
		trail: { label: string; href?: string }[];
	} = $props();

	const ALL_TABS = [
		{ id: 'summary', label: 'Summary' },
		{ id: 'vms', label: 'VMs' },
		{ id: 'monitor', label: 'Monitor' },
		{ id: 'configure', label: 'Configure' },
		{ id: 'permissions', label: 'Permissions' }
	];
	const tabs = $derived.by(() => {
		if (scope.kind === 'node') return ALL_TABS.filter((t) => t.id !== 'permissions');
		if (scope.kind === 'network' || scope.kind === 'storage')
			return ALL_TABS.filter((t) => t.id === 'summary' || t.id === 'vms');
		return ALL_TABS;
	});
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab');
		return tabs.some((x) => x.id === t) ? t! : 'summary';
	});

	// VMs in the current scope, feeding the grid. Network/storage membership uses
	// the same key helpers as the tree's grouping, so they can never disagree.
	const scopedVMs = $derived.by(() => {
		const inv = inventory.inventory;
		if (!inv) return [];
		const sc = scope; // const preserves TS narrowing into the filter closures
		const all = inventory.allVMs;
		if (sc.kind === 'all') return all;
		if (sc.kind === 'node') return all.filter((v) => (v.nodeName || '(unscheduled)') === sc.node);
		if (sc.kind === 'network')
			return all.filter((v) => vmNetworkKeys(v, inventory.networks).includes(sc.network));
		if (sc.kind === 'storage') return all.filter((v) => vmStorageKeys(v).includes(sc.storageClass));
		return inv.projects
			.filter((p) => p.name === sc.project)
			.flatMap((p) =>
				p.namespaces
					.filter((n) => sc.kind !== 'namespace' || n.namespace === sc.namespace)
					.flatMap((n) => n.vms)
			);
	});

	// Projects shown on the Configure tab (the scoped one, or all).
	const cfgProjects = $derived.by(() => {
		const inv = inventory.inventory;
		if (!inv) return [];
		const sc = scope;
		if (sc.kind === 'project' || sc.kind === 'namespace')
			return inv.projects.filter((p) => p.name === sc.project);
		if (sc.kind === 'node' || sc.kind === 'network' || sc.kind === 'storage') return [];
		return inv.projects;
	});
	// The metrics-backend scope. Network/storage lenses are navigation groupings,
	// not metrics boundaries — their Summary/Monitor aggregate the whole
	// inventory, like 'all'.
	const containerScope = $derived(
		scope.kind === 'project'
			? { project: scope.project }
			: scope.kind === 'namespace'
				? { project: scope.project, namespace: scope.namespace }
				: scope.kind === 'node'
					? { node: scope.node }
					: {}
	);
	const scopedNamespaces = $derived([...new Set(scopedVMs.map((v) => v.namespace))]);

	const openVM = (namespace: string, name: string) => goto(vmHref(namespace, name));

	// --- bulk actions over the grid selection (keys "namespace/name") ---
	// Deliberately independent of the VM action registry: Power isn't a registry
	// action, and these stage batch edits, not runtime ops.

	let picked = $state<Set<string>>(new Set());
	let confirmingBulkDelete = $state(false);
	let bulkBusy = $state(false);

	// The VM objects currently picked (resolve keys against the live inventory).
	const pickedVMs = $derived(
		inventory.allVMs.filter((vm) => picked.has(`${vm.namespace}/${vm.name}`))
	);

	// Bulk context menu for a right-click inside the multi-selection. Registered
	// with the shell while this workspace is mounted; the shell renders the
	// single-VM and container variants.
	let bulkCtx = $state<{ x: number; y: number } | null>(null);
	$effect(() => {
		ui.bulkIntercept = (vm, x, y) => {
			if (picked.size > 1 && picked.has(`${vm.namespace}/${vm.name}`)) {
				bulkCtx = { x, y };
				return true;
			}
			return false;
		};
		return () => (ui.bulkIntercept = null);
	});

	// Run one staging call per VM in parallel, tallying outcomes. `skip` filters
	// no-ops client-side; any per-VM failure folds into the skipped count rather
	// than aborting the batch.
	async function runBulk(
		vms: VM[],
		stage: (vm: VM) => Promise<unknown>,
		skip: (vm: VM) => boolean,
		verb: string
	) {
		if (bulkBusy) return;
		bulkBusy = true;
		try {
			const actionable = vms.filter((vm) => !skip(vm));
			const skipped = vms.length - actionable.length;
			const results = await Promise.allSettled(actionable.map((vm) => stage(vm)));
			if (results.some((r) => r.status === 'rejected' && r.reason instanceof Unauthorized)) {
				return; // signed out centrally by the api layer
			}
			const failed = results.filter((r) => r.status === 'rejected').length;
			const staged = results.length - failed;
			await drafts.refresh();
			picked = new Set();
			const extra = [skipped ? `${skipped} skipped` : '', failed ? `${failed} failed` : '']
				.filter(Boolean)
				.join(', ');
			ui.showToast(
				`${verb} ${staged} of ${vms.length}${extra ? ` (${extra})` : ''}.`,
				staged > 0 ? { label: 'Review & propose', run: () => (ui.changesOpen = true) } : undefined
			);
		} finally {
			bulkBusy = false;
		}
	}

	function bulkPower(target: 'On' | 'Off') {
		runBulk(
			pickedVMs,
			(vm) => api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, power: target }),
			// Already in target state, or not in git (cluster-only) → no-op.
			(vm) => vm.power === target || !vm.sourceFile,
			`Powered ${target.toLowerCase()}: staged`
		);
	}

	async function bulkDelete() {
		confirmingBulkDelete = false;
		await runBulk(
			pickedVMs,
			(vm) => api.stageDelete(vm.namespace, vm.name),
			(vm) => !vm.sourceFile, // not in git → nothing to stage a removal of
			'Deletion staged for'
		);
	}
</script>

<Breadcrumb {trail} />

<TabBar class="border-b border-line px-4" {tabs} active={tab} href={(t) => `?tab=${t}`} />

{#if tab === 'summary'}
	{#if scope.kind === 'network'}
		<SegmentSummary network={scope.network} vms={scopedVMs} />
	{:else if scope.kind === 'storage'}
		<StorageClassSummary storageClass={scope.storageClass} vms={scopedVMs} />
	{:else}
		{#if scope.kind === 'project' || scope.kind === 'namespace'}
			<PendingBanner project={scope.project} />
		{/if}
		<div class="min-h-0 flex-1 overflow-y-auto">
			<ClusterSummary scope={containerScope} onselect={openVM} />
		</div>
	{/if}
{:else if tab === 'monitor'}
	<div class="min-h-0 flex-1 overflow-y-auto">
		<ContainerMonitor namespaces={scopedNamespaces} scope={containerScope} onselect={openVM} />
	</div>
{:else if tab === 'permissions'}
	<div class="min-h-0 flex-1 overflow-y-auto p-4">
		<Permissions namespaces={scopedNamespaces} />
	</div>
{:else if tab === 'configure'}
	{#if scope.kind === 'node'}
		<NodeConfigure node={scope.node} vms={scopedVMs} />
	{:else}
		<ContainerConfigure
			projects={cfgProjects}
			cluster={scope.kind === 'all'}
			onstaged={() => drafts.refresh()}
		/>
	{/if}
{:else}
	{#if picked.size > 0}
		<BulkActionsBar
			count={picked.size}
			busy={bulkBusy}
			onpower={bulkPower}
			ondelete={() => (confirmingBulkDelete = true)}
			onclear={() => (picked = new Set())}
		/>
	{/if}
	<VMTable
		vms={scopedVMs}
		bind:selected={picked}
		staged={drafts.stagedByKey}
		onselect={(vm) => openVM(vm.namespace, vm.name)}
		onstagedopen={(vm) => (ui.modal = { kind: 'staged', vm })}
		oncontextvm={(vm, x, y) => ui.openVMContext(vm, x, y)}
	/>
{/if}

{#if bulkCtx}
	<ContextMenu x={bulkCtx.x} y={bulkCtx.y} onclose={() => (bulkCtx = null)}>
		<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
			<div class="px-3 py-1 text-[10px] tracking-wide text-ink-faint uppercase">
				{picked.size} VMs selected
			</div>
			<MenuItem
				onclick={() => {
					bulkCtx = null;
					bulkPower('On');
				}}>Power On (staged)</MenuItem
			>
			<MenuItem
				onclick={() => {
					bulkCtx = null;
					bulkPower('Off');
				}}>Power Off (staged)</MenuItem
			>
			<div class="my-1 border-t border-slate-100"></div>
			<MenuItem
				danger
				onclick={() => {
					bulkCtx = null;
					confirmingBulkDelete = true;
				}}>Delete {picked.size} VMs…</MenuItem
			>
			<div class="my-1 border-t border-slate-100"></div>
			<MenuItem
				onclick={() => {
					bulkCtx = null;
					picked = new Set();
				}}>Clear selection</MenuItem
			>
		</div>
	</ContextMenu>
{/if}

{#if confirmingBulkDelete}
	<ConfirmDelete
		title="Delete {pickedVMs.length} VMs"
		confirmWord="delete"
		busy={bulkBusy}
		onconfirm={bulkDelete}
		onclose={() => (confirmingBulkDelete = false)}
	>
		<p class="mb-3">
			This stages removal of the following VMs into <strong>Changes</strong>. They are deleted from
			the cluster only when each project's PR is merged.
		</p>
		<ul class="max-h-40 overflow-y-auto rounded border border-line text-xs">
			{#each pickedVMs as vm (vm.namespace + '/' + vm.name)}
				<li class="border-b border-slate-100 px-2 py-1 last:border-0">
					<span class="font-medium text-ink">{vm.name}</span>
					<span class="text-ink-faint">· {vm.namespace}</span>
				</li>
			{/each}
		</ul>
	</ConfirmDelete>
{/if}
