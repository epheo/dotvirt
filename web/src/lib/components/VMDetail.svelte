<script module lang="ts">
	// The detail view's tab ids, exported so the route guard validates ?tab=
	// against the same list the TabBar renders.
	export const VM_TABS = [
		'summary',
		'monitor',
		'configure',
		'security',
		'permissions',
		'snapshots',
		'console',
	] as const;
	export type VMTab = (typeof VM_TABS)[number];
</script>

<script lang="ts">
	import { untrack } from 'svelte';
	import {
		ArrowRightLeft,
		ChevronDown,
		Monitor,
		Pause,
		Pencil,
		Play,
		RotateCw,
		Server,
	} from 'lucide-svelte';
	import { api, Unauthorized, type Change, type DraftItem, type Network, type VM } from '$lib/api';
	import { adoptVM, manifestURL, runRuntimeAction, vmActions, type VMAction } from '$lib/actions';
	import { type EditSection } from '$lib/editform';
	import { action } from '$lib/resource.svelte';
	import { ui, type DetailAction } from '$lib/state/ui.svelte';
	import { duration, friendlyError } from '$lib/format';
	import { phaseTone } from '$lib/status';
	import ActionMenu from './ActionMenu.svelte';
	import Banner from './Banner.svelte';
	import CloneModal from './CloneModal.svelte';
	import MigrateModal from './MigrateModal.svelte';
	import StorageMigrateModal from './StorageMigrateModal.svelte';
	import SaveTemplateModal from './SaveTemplateModal.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';
	import Console from './Console.svelte';
	import EditSettings from './EditSettings.svelte';
	import EffectivePolicyPanel from './EffectivePolicyPanel.svelte';
	import HeaderMenu from './HeaderMenu.svelte';
	import TracePanel from './TracePanel.svelte';
	import MetricsPanel from './MetricsPanel.svelte';
	import PendingBanner from './PendingBanner.svelte';
	import Permissions from './Permissions.svelte';
	import Snapshots from './Snapshots.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import StatusDot from './StatusDot.svelte';
	import StatusPill from './StatusPill.svelte';
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
		onintentdone,
	}: {
		vm: VM | null;
		// The active tab is owned by the page (?tab=); ontab is the programmatic
		// switch (an action or intent jumping to Snapshots/Console).
		tab?: VMTab;
		ontab?: (t: VMTab) => void;
		onstaged?: () => void;
		stagedItem?: DraftItem | null;
		onstagedopen?: () => void;
		onsearchlabel?: (key: string, value: string) => void;
		// The port-group catalog (GET /api/networks), to resolve each NIC's raw
		// network ref into the port group the admin recognizes.
		networks?: Network[];
		// A one-shot request from outside (the context menu) to open a modal/tab
		// here; seq distinguishes repeated requests for the same id.
		intent?: { id: DetailAction; seq: number } | null;
		// Fired once the intent is applied so the owner clears it - this view
		// remounts whenever the VM drops out of an inventory frame and returns,
		// and a kept intent would replay (reopening a dismissed dialog).
		onintentdone?: () => void;
	} = $props();

	// Monitor sub-rail (all time-series live under Monitor).
	let monitorView = $state<'events' | 'performance'>('events');
	let editing = $state(false);
	// Which EditSettings section a Configure "Edit" jumps to (undefined = all).
	let editSection = $state<EditSection | undefined>(undefined);

	function openEdit(section?: EditSection) {
		editSection = section;
		editing = true;
	}

	// Delete is destructive once the PR merges, so it's gated behind a confirm
	// dialog that requires typing the VM name (handled by ConfirmDelete).
	let deleting = $state(false);
	const delOp = action();

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
	// surface as toasts - identical feedback to the right-click context menu.
	let runtimeBusy = $state(false);

	// The flat toolbar: the everyday imperative verbs, promoted out of the
	// Actions menu. Power is deliberately absent - it is a declarative
	// (staged, PR-gated) runStrategy change here, and a flat button would read
	// as immediate. Pause is the instant containment verb instead.
	const TOOLBAR: { id: VMAction['id']; icon: typeof Monitor; label: string }[] = [
		{ id: 'console', icon: Monitor, label: 'Console' },
		{ id: 'migrate', icon: ArrowRightLeft, label: 'Migrate' },
		{ id: 'restart', icon: RotateCw, label: 'Restart' },
		{ id: 'pause', icon: Pause, label: 'Pause' },
		{ id: 'unpause', icon: Play, label: 'Unpause' },
	];
	const toolbar = $derived.by(() => {
		const v = vm;
		if (!v) return [];
		return TOOLBAR.filter((t) => (v.paused ? t.id !== 'pause' : t.id !== 'unpause')).map((t) => ({
			...t,
			action: vmActions.find((a) => a.id === t.id)!,
		}));
	});
	const PROMOTED: VMAction['id'][] = ['console', 'migrate', 'restart', 'pause', 'unpause', 'edit'];

	function loadDrift(ns: string, name: string) {
		// Drop a stale response if the selection moved while it was in flight -
		// VM A's drift must never render under VM B.
		const fresh = () => vm?.namespace === ns && vm?.name === name;
		api
			.drift(ns, name)
			.then((d) => {
				if (fresh()) driftChanges = d.drift ? d.changes : [];
			})
			.catch(() => {
				if (fresh()) driftChanges = null; // a 401 signs out centrally via the api layer
			});
	}

	// One handler for every registry action: runtime ops run via the registry's
	// own run() (with busy/result reporting; the server records the task), host
	// actions map to this view's modals/tabs.
	async function handleAction(a: VMAction) {
		if (!vm) return;
		const target = vm;
		if (a.kind === 'runtime' && a.run) {
			runtimeBusy = true;
			try {
				await runRuntimeAction(a, target);
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

	// Maps a host-level action id onto this view's local UI state - the shared
	// tail of both entry points: the Actions menu (handleAction) and an outside
	// intent (context menu on an unselected VM). A new action is added here once.
	function applyAction(id: string) {
		switch (id) {
			case 'edit':
				openEdit();
				break;
			case 'delete':
				deleting = true;
				delOp.clear();
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
		// tab itself is URL state - a fresh VM route arrives without ?tab=.
		vmKey;
		untrack(() => {
			monitorView = 'events';
			editing = false;
			editSection = undefined;
			deleting = false;
			delOp.clear();
			cloning = false;
			templating = false;
			migrating = false;
			migratingStorage = false;
			driftChanges = null;
			if (vm) loadDrift(vm.namespace, vm.name);
		});
	});

	// Apply an outside intent (context menu -> "Edit settings" on an unselected
	// VM). Declared AFTER the reset effect above: when a selection change and an
	// intent arrive in the same flush, effects run in declaration order, so the
	// intent survives the reset.
	$effect(() => {
		const i = intent;
		if (!i) return;
		applyAction(i.id);
		onintentdone?.();
	});

	// adoptVM owns the toasts; this wrapper only feeds the Summary card's busy state.
	async function adopt() {
		if (!vm) return;
		reconciling = true;
		try {
			await adoptVM(vm, { onstaged });
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
		const target = vm;
		if (await delOp.run(() => api.stageDelete(target.namespace, target.name))) {
			deleting = false;
			onstaged?.();
		}
	}
</script>

{#if vm}
	<div class="flex h-full flex-col">
		<div class="border-b border-line px-4 pt-4">
			<div class="flex items-center gap-2.5">
				<Server size={20} class="shrink-0 text-ink-muted" />
				<h2 class="text-lg font-semibold text-ink">{vm.name}</h2>
				<StatusPill
					tone={phaseTone(vm.phase, vm.paused)}
					label={vm.paused ? 'Paused' : (vm.phase ?? String(vm.power))}
				/>
				<SyncBadge sync={vm.sync} error={vm.syncError} />
				{#if stagedItem}
					<StagedBadge item={stagedItem} onopen={() => onstagedopen?.()} />
				{/if}
				<span class="ml-1 flex min-w-0 items-center gap-1.5 truncate text-xs text-ink-faint">
					{vm.namespace}{#if vm.nodeName}<span class="text-line-strong">/</span
						>{vm.nodeName}{/if}{#if vm.instancetype}<span class="text-line-strong">/</span
						>{vm.instancetype}{/if}
				</span>
			</div>
			<div class="mt-1.5 mb-1 flex flex-wrap items-center gap-0.5">
				{#each toolbar as t (t.id)}
					{@const Icon = t.icon}
					<button
						onclick={() => handleAction(t.action)}
						disabled={!t.action.enabled(vm) || runtimeBusy}
						title={t.action.title ?? ''}
						class="flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-45 disabled:hover:bg-transparent"
					>
						<Icon size={13} />
						{t.label}
					</button>
				{/each}
				<span class="mx-1.5 h-4 w-px bg-line"></span>
				<button
					onclick={() => openEdit()}
					disabled={!vm.sourceFile}
					title={vm.sourceFile ? 'Edit settings' : 'Not in git — adopt this VM first'}
					class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50 disabled:hover:bg-transparent"
				>
					<Pencil size={13} /> Edit Settings
				</button>
				<HeaderMenu align="right" panel={false}>
					{#snippet trigger({ toggle })}
						<button
							onclick={toggle}
							disabled={runtimeBusy}
							title="Everything else — snapshots, clone, adopt, delete; config changes go through a PR"
							class="flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							Actions <ChevronDown size={13} />
						</button>
					{/snippet}
					{#snippet children({ close })}
						<ActionMenu
							{vm}
							exclude={PROMOTED}
							onpick={(a) => {
								close();
								handleAction(a);
							}}
						/>
					{/snippet}
				</HeaderMenu>
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
			<Banner tone="accent">
				<StatusDot tone="info" size="xs" pulse />
				Live-migrating{#if vm.migration.sourceNode}&nbsp;from {vm.migration.sourceNode}{/if}
				to {vm.migration.targetNode || '…'}{#if duration(vm.migration.startedAt)}&nbsp;· started {duration(
						vm.migration.startedAt,
					)} ago{/if}
			</Banner>
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
					onmonitor={() => ontab?.('monitor')}
					onedit={() => openEdit()}
				/>
			{:else if tab === 'monitor'}
				<!-- Monitor sub-rail: events + performance. -->
				<TabBar
					class="mb-3 border-b border-line"
					tabs={[
						{ id: 'events', label: 'Events' },
						{ id: 'performance', label: 'Performance' },
					]}
					active={monitorView}
					onchange={(v) => (monitorView = v as typeof monitorView)}
				/>
				{#if monitorView === 'performance'}
					{#key vmKey}
						<MetricsPanel load={(r) => api.metrics(vm.namespace, vm.name, r)} />
					{/key}
				{:else}
					<VMEventsTable {vm} />
				{/if}
			{:else if tab === 'configure'}
				<VMConfigure {vm} {networks} onedit={openEdit} {onsearchlabel} />
			{:else if tab === 'security'}
				<div class="max-w-3xl space-y-4">
					<section class="rounded border border-line bg-panel p-3">
						<h2 class="mb-2 text-sm font-semibold text-ink">Trace a flow from this VM</h2>
						{#key vmKey}
							<TracePanel source={{ namespace: vm.namespace, vm: vm.name }} />
						{/key}
					</section>
					<EffectivePolicyPanel namespace={vm.namespace} vm={vm.name} />
				</div>
			{:else if tab === 'permissions'}
				<Permissions namespaces={[vm.namespace]} />
			{:else if tab === 'snapshots'}
				{#key vmKey}
					<Snapshots {vm} />
				{/key}
			{:else}
				{#key vmKey}
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
			busy={delOp.busy}
			error={delOp.error}
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
