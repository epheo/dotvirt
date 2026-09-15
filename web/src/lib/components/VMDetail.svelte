<script lang="ts">
	import {
		ArrowRightLeft,
		ChevronDown,
		Monitor,
		Pause,
		Pencil,
		Play,
		RotateCw,
		Server,
		Square,
	} from 'lucide-svelte';
	import { api, vmKey, type Change, type DraftItem, type Network, type VM } from '$lib/api';
	import {
		adoptVM,
		manifestURL,
		openVMDialog,
		runRuntimeAction,
		vmActions,
		type VMAction,
	} from '$lib/actions';
	import { type EditSection } from '$lib/editform';
	import { type VMTab } from '$lib/nav';
	import { action, resource } from '$lib/resource.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import { duration } from '$lib/format';
	import { phaseTone } from '$lib/status';
	import ActionMenu from './ActionMenu.svelte';
	import Button from './Button.svelte';
	import Banner from './Banner.svelte';
	import Console from './Console.svelte';
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
	import VMChanges from './VMChanges.svelte';

	let {
		vm,
		tab = 'summary',
		ontab,
		stagedItem = null,
		onstagedopen,
		networks = [],
	}: {
		vm: VM;
		// The active tab is owned by the page (?tab=); ontab is the programmatic
		// switch (an action jumping to Snapshots/Console).
		tab?: VMTab;
		ontab?: (t: VMTab) => void;
		stagedItem?: DraftItem | null;
		onstagedopen?: () => void;
		// The port-group catalog (GET /api/networks), to resolve each NIC's raw
		// network ref into the port group the admin recognizes.
		networks?: Network[];
	} = $props();

	// Monitor sub-rail (all time-series live under Monitor).
	let monitorView = $state<'events' | 'performance'>('events');

	// section = the EditSettings step a Configure "Edit" jumps to (undefined = all).
	function openEdit(section?: EditSection) {
		ui.modal = { kind: 'editVM', vm, section };
	}

	// adopt/resync, feeding the Summary card's busy state.
	const reconcileOp = action({ toast: true });

	// Imperative runtime ops (restart/pause/unpause/live-migrate). Results
	// surface as toasts - identical feedback to the right-click context menu.
	const runtimeOp = action({ toast: true });

	// The flat toolbar: the everyday imperative verbs, promoted out of the
	// Actions menu. Power is deliberately absent - it is a declarative
	// (staged, PR-gated) runStrategy change here, and a flat button would read
	// as immediate. Pause is the instant containment verb instead.
	const TOOLBAR: { id: VMAction['id']; icon: typeof Monitor; label: string; sep?: boolean }[] = [
		{ id: 'restart', icon: RotateCw, label: 'Restart' },
		{ id: 'pause', icon: Pause, label: 'Pause' },
		{ id: 'unpause', icon: Play, label: 'Unpause' },
		{ id: 'console', icon: Monitor, label: 'Console', sep: true },
		{ id: 'migrate', icon: ArrowRightLeft, label: 'Migrate' },
	];
	const toolbar = $derived(
		TOOLBAR.filter((t) => (vm.paused ? t.id !== 'pause' : t.id !== 'unpause')).map((t) => ({
			...t,
			action: vmActions.find((a) => a.id === t.id)!,
		})),
	);
	const PROMOTED: VMAction['id'][] = ['console', 'migrate', 'restart', 'pause', 'unpause', 'edit'];

	// Power is declarative here (a staged runStrategy change), but it still
	// deserves a first-class button: hiding it inside Edit Settings made the
	// most basic verb the hardest to find. The button stages and says so.
	const powerOp = action({ toast: true });
	function stagePower() {
		const target = vm;
		if (!target.sourceFile || powerOp.busy) return;
		const to = target.power === 'On' ? 'Off' : 'On';
		return powerOp.run(async () => {
			await api.stageEdit(target.namespace, target.name, {
				sourceFile: target.sourceFile,
				power: to,
			});
			ui.toastStaged(`Power ${to} staged for ${target.name} — applies when the PR merges.`);
		});
	}

	// One handler for every registry action: runtime ops run via the registry's
	// own run() (with busy/result reporting; the server records the task), host
	// actions open their dialog or switch a tab.
	async function handleAction(a: VMAction) {
		const target = vm;
		if (a.kind === 'runtime' && a.run) {
			await runtimeOp.run(() => runRuntimeAction(a, target));
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
			case 'console':
				ontab?.('console');
				break;
			case 'snapshot':
				ontab?.('snapshots');
				break;
			default:
				openVMDialog(a.id, target);
		}
	}

	// The reset keys on the selection's IDENTITY, not the vm object: every live
	// inventory frame hands down a fresh object for the same VM, and resetting
	// on reference would snap the Monitor rail back and refetch drift whenever
	// cluster state moves. The tab itself is URL state - a fresh VM route
	// arrives without ?tab=.
	const key = $derived(vmKey(vm));
	$effect(() => {
		key;
		monitorView = 'events';
	});
	// Drift detail (running vs main): null until known, [] when identical.
	const driftRes = resource<Change[]>(
		() => key,
		() => api.drift(vm.namespace, vm.name).then((d) => (d.drift ? d.changes : [])),
		{ reset: true },
	);
	const driftChanges = $derived(driftRes.data);

	// adoptVM owns its toasts; the run only feeds the busy state.
	const adopt = () => reconcileOp.run(() => adoptVM(vm));

	function resync() {
		return reconcileOp.run(async () => {
			const r = await api.resync(vm.namespace, vm.name);
			ui.showToast(`Re-sync triggered on ArgoCD app "${r.application}".`, { kind: 'success' });
		});
	}
</script>

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
			<button
				onclick={stagePower}
				disabled={!vm.sourceFile || powerOp.busy}
				title={vm.sourceFile
					? 'Stages a power change into a PR — nothing happens until it merges'
					: 'Not in git — adopt this VM first'}
				class="flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-45 disabled:hover:bg-transparent"
			>
				{#if vm.power === 'On'}<Square size={13} /> Power off{:else}<Play size={13} /> Power on{/if}
			</button>
			{#each toolbar as t (t.id)}
				{@const Icon = t.icon}
				{#if t.sep}<span class="mx-1.5 h-4 w-px bg-line"></span>{/if}
				<button
					onclick={() => handleAction(t.action)}
					disabled={!t.action.enabled(vm) || runtimeOp.busy}
					title={t.action.title ?? ''}
					class="flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-45 disabled:hover:bg-transparent"
				>
					<Icon size={13} />
					{t.label}
				</button>
			{/each}
			<span class="mx-1.5 h-4 w-px bg-line"></span>
			<Button
				variant="secondary"
				size="sm"
				onclick={() => openEdit()}
				disabled={!vm.sourceFile}
				title={vm.sourceFile ? 'Edit settings' : 'Not in git — adopt this VM first'}
			>
				<Pencil size={13} /> Edit Settings
			</Button>
			<HeaderMenu align="right" panel={false}>
				{#snippet trigger({ toggle })}
					<button
						onclick={toggle}
						disabled={runtimeOp.busy}
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
				{ id: 'changes', label: 'Changes' },
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
				reconciling={reconcileOp.busy}
				onadopt={adopt}
				onresync={resync}
				onconsole={() => ontab?.('console')}
				onmonitor={() => ontab?.('monitor')}
				onedit={() => openEdit()}
				onmigrate={() => openVMDialog('migrate', vm)}
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
				{#key key}
					<MetricsPanel load={(r) => api.metrics(vm.namespace, vm.name, r)} />
				{/key}
			{:else}
				<VMEventsTable {vm} />
			{/if}
		{:else if tab === 'configure'}
			<VMConfigure {vm} {networks} onedit={openEdit} />
		{:else if tab === 'security'}
			<div class="max-w-3xl space-y-4">
				<section class="rounded border border-line bg-panel p-3">
					<h2 class="mb-2 text-sm font-semibold text-ink">Trace a flow from this VM</h2>
					{#key key}
						<TracePanel source={{ namespace: vm.namespace, vm: vm.name }} />
					{/key}
				</section>
				<EffectivePolicyPanel namespace={vm.namespace} vm={vm.name} />
			</div>
		{:else if tab === 'permissions'}
			<Permissions namespaces={[vm.namespace]} />
		{:else if tab === 'changes'}
			{#key key}
				<VMChanges {vm} {stagedItem} />
			{/key}
		{:else if tab === 'snapshots'}
			{#key key}
				<Snapshots {vm} />
			{/key}
		{:else}
			{#key key}
				<Console {vm} />
			{/key}
		{/if}
	</div>
</div>
