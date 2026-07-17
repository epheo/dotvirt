<script lang="ts">
	import { untrack } from 'svelte';
	import { ChevronDown, Pencil, Trash2 } from 'lucide-svelte';
	import { api, Unauthorized, type Change, type DraftItem, type Network, type VM } from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import { ui, type DetailAction } from '$lib/state/ui.svelte';
	import { duration, friendlyError } from '$lib/format';
	import ActionMenu from './ActionMenu.svelte';
	import CloneModal from './CloneModal.svelte';
	import MigrateModal from './MigrateModal.svelte';
	import StorageMigrateModal from './StorageMigrateModal.svelte';
	import SaveTemplateModal from './SaveTemplateModal.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';
	import Console from './Console.svelte';
	import EditSettings from './EditSettings.svelte';
	import EffectivePolicyPanel from './EffectivePolicyPanel.svelte';
	import MetricsPanel from './MetricsPanel.svelte';
	import PendingBanner from './PendingBanner.svelte';
	import Permissions from './Permissions.svelte';
	import PowerDot from './PowerDot.svelte';
	import Snapshots from './Snapshots.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import TabBar from './TabBar.svelte';
	import VMConfigure from './VMConfigure.svelte';
	import VMEventsTable from './VMEventsTable.svelte';
	import VMSummary from './VMSummary.svelte';

	let {
		vm,
		tab = 'summary',
		ontab,
		onstaged,
		stagedItem = null,
		onstagedopen,
		onsearchlabel,
		networks = [],
		intent = null,
	}: {
		vm: VM | null;
		// The active tab is owned by the page (?tab=); ontab is the programmatic
		// switch (an action or intent jumping to Snapshots/Console).
		tab?: Tab;
		ontab?: (t: Tab) => void;
		onstaged?: () => void;
		stagedItem?: DraftItem | null;
		onstagedopen?: () => void;
		onsearchlabel?: (key: string, value: string) => void;
		// The port-group catalog (GET /api/networks), to resolve each NIC's raw
		// network ref into the vCenter port group the admin recognizes.
		networks?: Network[];
		// A one-shot request from outside (the context menu) to open a modal/tab
		// here; seq distinguishes repeated requests for the same id.
		intent?: { id: DetailAction; seq: number } | null;
	} = $props();

	type Tab =
		'summary' | 'monitor' | 'configure' | 'security' | 'permissions' | 'snapshots' | 'console';

	// Monitor sub-rail (vCenter keeps all time-series under Monitor).
	let monitorView = $state<'events' | 'performance'>('events');
	let editing = $state(false);
	// Which EditSettings section a Configure "Edit" jumps to (undefined = all).
	let editSection = $state<'compute' | 'storage' | 'network' | 'labels' | undefined>(undefined);

	function openEdit(section?: 'compute' | 'storage' | 'network' | 'labels') {
		editSection = section;
		editing = true;
	}

	// Delete is destructive once the PR merges, so it's gated behind a confirm
	// dialog that requires typing the VM name (handled by ConfirmDelete).
	let deleting = $state(false);
	let deleteBusy = $state(false);
	let deleteErr = $state('');

	// Clone name-prompt modal (creates a VirtualMachineClone; imperative).
	let cloning = $state(false);
	let templating = $state(false);

	// Live-migration target picker (imperative) and storage migration (PR-gated).
	let migrating = $state(false);
	let migratingStorage = $state(false);

	// Drift detail (running vs main) for the selected VM.
	let driftChanges = $state<Change[] | null>(null);
	let reconciling = $state(false);

	// Imperative runtime ops (restart/pause/unpause/live-migrate). Results
	// surface as toasts — identical feedback to the right-click context menu.
	let actionsOpen = $state(false);
	let runtimeBusy = $state(false);

	function loadDrift(ns: string, name: string) {
		api
			.drift(ns, name)
			.then((d) => (driftChanges = d.drift ? d.changes : []))
			.catch(() => (driftChanges = null)); // a 401 signs out centrally via the api layer
	}

	// One handler for every registry action: runtime ops run via the registry's
	// own run() (with busy/result reporting; the server records the task), host
	// actions map to this view's modals/tabs.
	async function handleAction(a: VMAction) {
		actionsOpen = false;
		if (!vm) return;
		const target = vm;
		if (a.kind === 'runtime' && a.run) {
			runtimeBusy = true;
			const verb = a.verb ?? a.label;
			try {
				await a.run(target);
				ui.showToast(`${verb} requested for ${target.name}.`, { kind: 'success' });
			} catch (e) {
				if (e instanceof Unauthorized) return; // signed out centrally; skip the error toast
				ui.showToast(`${verb} failed for ${target.name}: ${friendlyError(e)}`, { kind: 'error' });
			} finally {
				runtimeBusy = false;
			}
			return;
		}
		switch (a.id) {
			case 'adopt':
				adopt();
				break;
			case 'manifest':
				// A plain navigation: the route is cookie-auth'd and sets
				// Content-Disposition, so the browser downloads the YAML.
				window.open(manifestURL(target), '_blank');
				break;
			default:
				applyAction(a.id);
		}
	}

	// Maps a host-level action id onto this view's local UI state — the shared
	// tail of both entry points: the Actions menu (handleAction) and an outside
	// intent (context menu on an unselected VM). A new action is added here once.
	function applyAction(id: string) {
		switch (id) {
			case 'edit':
				openEdit();
				break;
			case 'delete':
				deleting = true;
				deleteErr = '';
				break;
			case 'console':
				ontab?.('console');
				break;
			case 'snapshot':
				ontab?.('snapshots');
				break;
			case 'clone':
				cloning = true;
				break;
			case 'template':
				templating = true;
				break;
			case 'migrate':
				migrating = true;
				break;
			case 'migrate-storage':
				migratingStorage = true;
				break;
		}
	}

	// The reset keys on the selection's IDENTITY, not the vm object: every live
	// inventory frame hands down a fresh object for the same VM, and resetting
	// on reference would snap tabs back to Summary and close modals whenever
	// cluster state moves (e.g. mid-clone, mid-migration).
	const vmKey = $derived(vm ? `${vm.namespace}/${vm.name}` : '');
	$effect(() => {
		// Reset when the selection changes, and (re)load drift for this VM. The
		// tab itself is URL state — a fresh VM route arrives without ?tab=.
		vmKey;
		untrack(() => {
			monitorView = 'events';
			editing = false;
			editSection = undefined;
			deleting = false;
			deleteErr = '';
			cloning = false;
			templating = false;
			migrating = false;
			migratingStorage = false;
			driftChanges = null;
			actionsOpen = false;
			if (vm) loadDrift(vm.namespace, vm.name);
		});
	});

	// Apply an outside intent (context menu → "Edit settings" on an unselected
	// VM). Declared AFTER the reset effect above: when a selection change and an
	// intent arrive in the same flush, effects run in declaration order, so the
	// intent survives the reset.
	$effect(() => {
		const i = intent;
		if (!i) return;
		applyAction(i.id);
	});

	async function adopt() {
		if (!vm) return;
		reconciling = true;
		try {
			await api.adopt(vm.namespace, vm.name);
			ui.showToast('Live state staged into Changes — open a PR to adopt it into git.', {
				kind: 'success',
				action: { label: 'Review & propose', run: () => (ui.changesOpen = true) },
			});
			onstaged?.();
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the error toast
			ui.showToast(friendlyError(e), { kind: 'error' });
		} finally {
			reconciling = false;
		}
	}

	async function resync() {
		if (!vm) return;
		reconciling = true;
		try {
			const r = await api.resync(vm.namespace, vm.name);
			ui.showToast(`Re-sync triggered on ArgoCD app "${r.application}".`, { kind: 'success' });
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the error toast
			ui.showToast(friendlyError(e), { kind: 'error' });
		} finally {
			reconciling = false;
		}
	}

	async function confirmDelete() {
		if (!vm) return;
		deleteBusy = true;
		deleteErr = '';
		try {
			await api.stageDelete(vm.namespace, vm.name);
			deleting = false;
			onstaged?.();
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the error banner
			deleteErr = String(e);
		} finally {
			deleteBusy = false;
		}
	}
</script>

{#if vm}
	<div class="flex h-full flex-col">
		<div class="border-b border-line px-4 pt-4">
			<div class="mb-3 flex items-center gap-2">
				<PowerDot power={vm.power} paused={vm.paused} />
				<h2 class="text-lg font-semibold text-ink">{vm.name}</h2>
				<span class="rounded bg-line px-1.5 py-0.5 text-xs text-ink-soft">{vm.namespace}</span>
				<SyncBadge sync={vm.sync} error={vm.syncError} />
				{#if stagedItem}
					<StagedBadge item={stagedItem} onopen={() => onstagedopen?.()} />
				{/if}
				<div class="ml-auto flex items-center gap-2">
					<div class="relative">
						<button
							onclick={() => (actionsOpen = !actionsOpen)}
							disabled={runtimeBusy}
							title="All VM actions — runtime ops act immediately; config changes go through a PR"
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							Actions <ChevronDown size={13} />
						</button>
						{#if actionsOpen}
							<button
								class="fixed inset-0 z-10 cursor-default"
								onclick={() => (actionsOpen = false)}
								aria-label="Close menu"
							></button>
							<div class="absolute right-0 z-20 mt-1">
								<ActionMenu {vm} onpick={handleAction} />
							</div>
						{/if}
					</div>
					<button
						onclick={() => openEdit()}
						disabled={!vm.sourceFile}
						title={vm.sourceFile ? 'Edit settings' : 'Not in git — adopt this VM first'}
						class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50 disabled:hover:bg-transparent"
					>
						<Pencil size={13} /> Edit Settings
					</button>
					<button
						onclick={() => {
							deleting = true;
							deleteErr = '';
						}}
						disabled={!vm.sourceFile}
						title={vm.sourceFile
							? 'Delete this VM (stages a removal into Changes)'
							: 'Not in git — adopt this VM first'}
						class="flex items-center gap-1.5 rounded border border-danger/50 px-2.5 py-1 text-xs font-medium text-danger-ink hover:bg-danger-soft/60 disabled:opacity-50 disabled:hover:bg-transparent"
					>
						<Trash2 size={13} /> Delete VM
					</button>
				</div>
			</div>
			<TabBar
				tabs={[
					{ id: 'summary', label: 'Summary' },
					{ id: 'monitor', label: 'Monitor' },
					{ id: 'configure', label: 'Configure' },
					{ id: 'security', label: 'Security' },
					{ id: 'permissions', label: 'Permissions' },
					{ id: 'snapshots', label: 'Snapshots' },
					{ id: 'console', label: 'Console' },
				]}
				active={tab}
				href={(t) => `?tab=${t}`}
			/>
		</div>

		{#if vm.migration && !vm.migration.completed && !vm.migration.failed}
			<div
				class="flex items-center gap-2 border-b border-select bg-select-soft px-4 py-1.5 text-xs text-accent-ink"
			>
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-accent"></span>
				Live-migrating{#if vm.migration.sourceNode}&nbsp;from {vm.migration.sourceNode}{/if}
				to {vm.migration.targetNode || '…'}{#if duration(vm.migration.startedAt)}&nbsp;· started {duration(
						vm.migration.startedAt,
					)} ago{/if}
			</div>
		{/if}

		<PendingBanner {vm} />

		<div class="min-h-0 flex-1 overflow-y-auto p-4">
			{#if tab === 'summary'}
				<VMSummary
					{vm}
					{stagedItem}
					{driftChanges}
					{reconciling}
					onadopt={adopt}
					onresync={resync}
					onconsole={() => ontab?.('console')}
				/>
			{:else if tab === 'monitor'}
				<!-- Monitor sub-rail: events + performance, vCenter's time-series home. -->
				<div class="mb-3 flex gap-1 border-b border-line text-sm">
					{#each ['events', 'performance'] as const as v (v)}
						<button
							class="border-b-2 px-3 py-1 capitalize {monitorView === v
								? 'border-accent text-accent-ink'
								: 'border-transparent text-ink-muted hover:text-ink-soft'}"
							onclick={() => (monitorView = v)}
						>
							{v}
						</button>
					{/each}
				</div>
				{#if monitorView === 'performance'}
					{#key `${vm.namespace}/${vm.name}`}
						<MetricsPanel load={(r) => api.metrics(vm.namespace, vm.name, r)} />
					{/key}
				{:else}
					<VMEventsTable {vm} />
				{/if}
			{:else if tab === 'configure'}
				<VMConfigure {vm} {networks} onedit={openEdit} {onsearchlabel} />
			{:else if tab === 'security'}
				<EffectivePolicyPanel namespace={vm.namespace} vm={vm.name} />
			{:else if tab === 'permissions'}
				<Permissions namespaces={[vm.namespace]} />
			{:else if tab === 'snapshots'}
				{#key `${vm.namespace}/${vm.name}`}
					<Snapshots {vm} />
				{/key}
			{:else}
				{#key `${vm.namespace}/${vm.name}`}
					<Console {vm} />
				{/key}
			{/if}
		</div>
	</div>

	{#if editing}
		<EditSettings
			{vm}
			{networks}
			initialSection={editSection}
			onclose={() => (editing = false)}
			onstaged={() => onstaged?.()}
		/>
	{/if}

	{#if cloning}
		<CloneModal {vm} onclose={() => (cloning = false)} />
	{/if}

	{#if migrating}
		<MigrateModal
			{vm}
			onclose={() => (migrating = false)}
			ondone={(ok) => {
				if (ok) ui.showToast(`Live-migration requested for ${vm.name}.`, { kind: 'success' });
			}}
		/>
	{/if}

	{#if migratingStorage}
		<StorageMigrateModal
			{vm}
			onclose={() => (migratingStorage = false)}
			onstaged={() => onstaged?.()}
		/>
	{/if}

	{#if templating}
		<SaveTemplateModal {vm} onclose={() => (templating = false)} onstaged={() => onstaged?.()} />
	{/if}

	{#if deleting}
		<ConfirmDelete
			title="Delete VM — {vm.name}"
			confirmWord={vm.name}
			busy={deleteBusy}
			error={deleteErr}
			onconfirm={confirmDelete}
			onclose={() => (deleting = false)}
		>
			<p>
				This removes <span class="font-mono text-xs">{vm.sourceFile}</span> from git and stages the
				change into <strong>Changes</strong>. The VM is deleted from the cluster only when the pull
				request is merged.
			</p>
		</ConfirmDelete>
	{/if}
{:else}
	<div class="flex h-full items-center justify-center text-sm text-ink-faint">
		Select a VM from the inventory
	</div>
{/if}
