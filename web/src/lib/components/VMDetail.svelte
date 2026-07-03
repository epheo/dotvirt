<script lang="ts">
	import { untrack } from 'svelte';
	import {
		Activity,
		ChevronDown,
		ChevronRight,
		Cpu,
		HardDrive,
		MemoryStick,
		Pencil,
		Trash2
	} from 'lucide-svelte';
	import {
		api,
		Unauthorized,
		type Change,
		type DraftItem,
		type Network,
		type VM,
		type VMEvent
	} from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import { duration } from '$lib/format';
	import ActionMenu from './ActionMenu.svelte';
	import ChangeList from './ChangeList.svelte';
	import CloneModal from './CloneModal.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';
	import CapacityUsage from './CapacityUsage.svelte';
	import Console from './Console.svelte';
	import ConsolePreview from './ConsolePreview.svelte';
	import EditSettings from './EditSettings.svelte';
	import InfoCard from './InfoCard.svelte';
	import MetricsPanel from './MetricsPanel.svelte';
	import Permissions from './Permissions.svelte';
	import PowerDot from './PowerDot.svelte';
	import Snapshots from './Snapshots.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import Row from './Row.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import VMConfigure from './VMConfigure.svelte';

	let {
		vm,
		onstaged,
		onaction,
		stagedItem = null,
		onstagedopen,
		onsearchlabel,
		networks = [],
		intent = null
	}: {
		vm: VM | null;
		onstaged?: () => void;
		onaction?: (a: { verb: string; namespace: string; name: string; ok: boolean }) => void;
		stagedItem?: DraftItem | null;
		onstagedopen?: () => void;
		onsearchlabel?: (key: string, value: string) => void;
		// The port-group catalog (GET /api/networks), to resolve each NIC's raw
		// network ref into the vCenter port group the admin recognizes.
		networks?: Network[];
		// A one-shot request from outside (the context menu) to open a modal/tab
		// here; seq distinguishes repeated requests for the same id.
		intent?: { id: 'edit' | 'delete' | 'console' | 'snapshot' | 'clone'; seq: number } | null;
	} = $props();

	type Tab = 'summary' | 'monitor' | 'configure' | 'permissions' | 'snapshots' | 'console';
	let tab = $state<Tab>('summary');
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

	// Drift detail (running vs main) for the selected VM.
	let driftChanges = $state<Change[] | null>(null);
	let showDrift = $state(false);
	let reconciling = $state(false);
	let reconcileMsg = $state('');
	let reconcileOk = $state(true);

	// Monitor tab: lazily-loaded Kubernetes events for the selected VM.
	let events = $state<VMEvent[] | null>(null);
	let eventsLoading = $state(false);

	// Imperative runtime ops (restart/pause/unpause/live-migrate).
	let actionsOpen = $state(false);
	let runtimeBusy = $state(false);
	let runtimeMsg = $state('');
	let runtimeOk = $state(true);

	// A paused VMI keeps phase Running, so the label checks the Paused flag too.
	// Action enablement lives in the registry ($lib/actions), not here.
	const statusText = $derived(vm ? (vm.paused ? 'Paused' : (vm.phase ?? vm.power)) : '');

	// Staged changes for this VM, keyed by field label (for inline current→future).
	const stagedChanges = $derived.by(() => {
		const m = new Map<string, Change>();
		for (const c of stagedItem?.changes ?? []) m.set(c.field, c);
		return m;
	});

	function loadDrift(ns: string, name: string) {
		api
			.drift(ns, name)
			.then((d) => (driftChanges = d.drift ? d.changes : []))
			.catch(() => (driftChanges = null)); // a 401 signs out centrally via the api layer
	}

	function loadEvents(ns: string, name: string) {
		eventsLoading = true;
		api
			.events(ns, name)
			.then((e) => (events = e))
			.catch(() => (events = []))
			.finally(() => (eventsLoading = false));
	}

	// Lazy-load events the first time the Monitor tab is opened for this VM.
	$effect(() => {
		if (vm && tab === 'monitor' && monitorView === 'events' && events === null && !eventsLoading) {
			loadEvents(vm.namespace, vm.name);
		}
	});

	// One handler for every registry action: runtime ops run via the registry's
	// own run() (with busy/result reporting + the task log), host actions map to
	// this view's modals/tabs.
	async function handleAction(a: VMAction) {
		actionsOpen = false;
		if (!vm) return;
		const target = vm;
		if (a.kind === 'runtime' && a.run) {
			runtimeBusy = true;
			runtimeMsg = '';
			let ok = true;
			try {
				await a.run(target);
				runtimeMsg = `${a.verb} requested — watch the Monitor tab for progress.`;
			} catch (e) {
				if (e instanceof Unauthorized) return; // signed out centrally; skip the error banner
				ok = false;
				runtimeMsg = String(e);
			} finally {
				runtimeBusy = false;
			}
			runtimeOk = ok;
			onaction?.({ verb: a.verb ?? a.label, namespace: target.namespace, name: target.name, ok });
			return;
		}
		switch (a.id) {
			case 'adopt':
				adopt();
				break;
			case 'edit':
				openEdit();
				break;
			case 'delete':
				deleting = true;
				deleteErr = '';
				break;
			case 'snapshot':
				tab = 'snapshots';
				break;
			case 'clone':
				cloning = true;
				break;
			case 'console':
				tab = 'console';
				break;
			case 'manifest':
				// A plain navigation: the route is cookie-auth'd and sets
				// Content-Disposition, so the browser downloads the YAML.
				window.open(manifestURL(target), '_blank');
				break;
		}
	}

	// The reset keys on the selection's IDENTITY, not the vm object: every live
	// inventory frame hands down a fresh object for the same VM, and resetting
	// on reference would snap tabs back to Summary and close modals whenever
	// cluster state moves (e.g. mid-clone, mid-migration).
	const vmKey = $derived(vm ? `${vm.namespace}/${vm.name}` : '');
	$effect(() => {
		// Reset when the selection changes, and (re)load drift for this VM.
		vmKey;
		untrack(() => {
			tab = 'summary';
			monitorView = 'events';
			editing = false;
			editSection = undefined;
			deleting = false;
			deleteErr = '';
			cloning = false;
			driftChanges = null;
			showDrift = false;
			reconcileMsg = '';
			events = null;
			eventsLoading = false;
			actionsOpen = false;
			runtimeMsg = '';
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
		switch (i.id) {
			case 'edit':
				openEdit();
				break;
			case 'delete':
				deleting = true;
				deleteErr = '';
				break;
			case 'console':
				tab = 'console';
				break;
			case 'snapshot':
				tab = 'snapshots';
				break;
			case 'clone':
				cloning = true;
				break;
		}
	});

	async function adopt() {
		if (!vm) return;
		reconciling = true;
		reconcileMsg = '';
		try {
			await api.adopt(vm.namespace, vm.name);
			reconcileMsg = 'Live state staged into Changes — open a PR to adopt it into git.';
			reconcileOk = true;
			onstaged?.();
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the error banner
			reconcileMsg = String(e);
			reconcileOk = false;
		} finally {
			reconciling = false;
		}
	}

	async function resync() {
		if (!vm) return;
		reconciling = true;
		reconcileMsg = '';
		try {
			const r = await api.resync(vm.namespace, vm.name);
			reconcileMsg = `Re-sync triggered on ArgoCD app "${r.application}".`;
			reconcileOk = true;
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the error banner
			reconcileMsg = String(e);
			reconcileOk = false;
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
		<div class="border-b border-slate-200 px-4 pt-4">
			<div class="mb-3 flex items-center gap-2">
				<PowerDot power={vm.power} paused={vm.paused} />
				<h2 class="text-lg font-semibold text-slate-800">{vm.name}</h2>
				<span class="rounded bg-slate-200 px-1.5 py-0.5 text-xs text-slate-600">{vm.namespace}</span
				>
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
							class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
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
						class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50 disabled:hover:bg-transparent"
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
						class="flex items-center gap-1.5 rounded border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50 disabled:opacity-50 disabled:hover:bg-transparent"
					>
						<Trash2 size={13} /> Delete VM
					</button>
				</div>
			</div>
			<nav class="flex gap-1 text-sm">
				{#each ['summary', 'monitor', 'configure', 'permissions', 'snapshots', 'console'] as const as t (t)}
					<button
						class="border-b-2 px-3 py-1.5 capitalize {tab === t
							? 'border-blue-600 text-blue-700'
							: 'border-transparent text-slate-500 hover:text-slate-700'}"
						onclick={() => (tab = t)}
					>
						{t}
					</button>
				{/each}
			</nav>
		</div>

		{#if vm.migration && !vm.migration.completed && !vm.migration.failed}
			<div
				class="flex items-center gap-2 border-b border-blue-200 bg-blue-50 px-4 py-1.5 text-xs text-blue-700"
			>
				<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-blue-500"></span>
				Live-migrating{#if vm.migration.sourceNode}&nbsp;from {vm.migration.sourceNode}{/if}
				to {vm.migration.targetNode || '…'}{#if duration(vm.migration.startedAt)}&nbsp;· started {duration(
						vm.migration.startedAt
					)} ago{/if}
			</div>
		{/if}

		{#if runtimeMsg}
			<div
				class="border-b px-4 py-1.5 text-xs {runtimeOk
					? 'border-slate-200 bg-slate-50 text-slate-600'
					: 'border-red-200 bg-red-50 text-red-700'}"
			>
				{runtimeMsg}
			</div>
		{/if}

		<div class="min-h-0 flex-1 overflow-y-auto p-4">
			{#if tab === 'summary'}
				<!-- At-a-glance tiles: the vCenter-style capacity summary. -->
				<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500">
							<Cpu size={13} /> CPU
						</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">
							{#if stagedChanges.has('CPU')}<span class="text-slate-400 line-through"
									>{vm.cpuCores ?? '—'} vCPU</span
								>
								<span class="text-blue-600">{stagedChanges.get('CPU')?.to}</span
								>{:else}{vm.cpuCores ?? '—'}<span class="ml-1 text-sm font-normal text-slate-500"
									>vCPU</span
								>{/if}
						</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500">
							<MemoryStick size={13} /> Memory
						</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">
							{#if stagedChanges.has('Memory')}<span class="text-slate-400 line-through"
									>{vm.memory ?? '—'}</span
								>
								<span class="text-blue-600">{stagedChanges.get('Memory')?.to}</span
								>{:else}{vm.memory ?? '—'}{/if}
						</div>
						{#if vm.memoryActual && vm.memoryActual !== vm.memory}
							<div class="text-xs text-slate-400">{vm.memoryActual} live</div>
						{/if}
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500">
							<HardDrive size={13} /> Disks
						</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{vm.disks?.length ?? 0}</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500">
							<Activity size={13} /> Status
						</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{statusText}</div>
						{#if duration(vm.startedAt)}<div class="text-xs text-slate-400">
								up {duration(vm.startedAt)}
							</div>{/if}
					</div>
				</div>

				<!-- Live usage bars (vCenter "Capacity and Usage") + the console preview
				     thumbnail (running VMs only). Side by side when there's room, stacked
				     on narrow; the preview emits no DOM when hidden, so capacity reclaims
				     the full width. -->
				<div class="mt-4 flex flex-col gap-4 xl:flex-row xl:items-start">
					<div class="min-w-0 flex-1">
						<CapacityUsage {vm} />
					</div>
					<ConsolePreview {vm} onopen={() => (tab = 'console')} />
				</div>

				<div class="mt-4 grid gap-4 md:grid-cols-2">
					<!-- Guest & runtime: live identity reported by the guest agent. -->
					<InfoCard title="Guest & runtime">
						<dl class="divide-y divide-slate-100 text-[13px]">
							<Row label="Operating system" value={vm.os ?? ''} />
							<Row label="Power (desired)">
								{#if stagedChanges.has('Power')}<span class="text-slate-400 line-through"
										>{vm.power}</span
									>
									<span class="text-blue-600">→ {stagedChanges.get('Power')?.to}</span>{:else}<span
										class="text-slate-800">{vm.power}</span
									>{/if}
							</Row>
							<Row label="Status (actual)" value={vm.paused ? 'Paused' : (vm.phase ?? '')} />
							<Row label="IP addresses">
								<div class="font-mono text-xs text-slate-800">
									{#if vm.ips?.length}
										{#each vm.ips as ip (ip)}<div>{ip}</div>{/each}
									{:else}{vm.guestIP || '—'}{/if}
								</div>
							</Row>
						</dl>
					</InfoCard>

					<!-- Configuration & placement: desired config + where it runs. -->
					<InfoCard title="Configuration & placement">
						<dl class="divide-y divide-slate-100 text-[13px]">
							<Row label="Instance type" value={vm.instancetype ?? ''} />
							<Row label="Preference" value={vm.preference ?? ''} />
							<Row label="Node" value={vm.nodeName ?? ''} />
							<Row label="Source" value={vm.sourceFile} mono />
						</dl>
					</InfoCard>
				</div>

				{#if !vm.sourceFile}
					<!-- Cluster-only VM (e.g. a fresh clone target): no manifest on the
					     base branch, so config stays read-only until adopted. The adopt
					     stages a CREATE of the running-branch manifest into the PR flow. -->
					<div class="mt-4 rounded border border-amber-200 bg-amber-50 px-3 py-2">
						<div class="flex items-center gap-2 text-sm font-medium text-amber-800">
							<span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>
							Not in git — this VM exists only in the cluster
						</div>
						<p class="mt-1 text-xs text-amber-700">
							A clone target (or out-of-band create) has no manifest on the base branch yet: config
							edits and ArgoCD sync don't apply. Adopting stages its live manifest into
							<strong>Changes</strong>, to propose as a PR.
						</p>
						<div class="mt-2">
							<button
								onclick={adopt}
								disabled={reconciling}
								title="Stage this VM's live manifest into a PR so git starts tracking it"
								class="rounded border border-amber-400 bg-white px-2.5 py-1 text-xs font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50"
							>
								Adopt into git
							</button>
						</div>
						{#if reconcileMsg}
							<p class="mt-2 text-xs {reconcileOk ? 'text-slate-600' : 'text-red-700'}">
								{reconcileMsg}
							</p>
						{/if}
					</div>
				{/if}

				{#if driftChanges && driftChanges.length > 0}
					<div class="mt-4 rounded border border-amber-200 bg-amber-50">
						<button
							onclick={() => (showDrift = !showDrift)}
							class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium text-amber-800"
						>
							<span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>
							Drift — cluster differs from git ({driftChanges.length})
							<span class="ml-auto text-amber-600"
								>{#if showDrift}<ChevronDown size={14} />{:else}<ChevronRight
										size={14}
									/>{/if}</span
							>
						</button>
						{#if showDrift}
							<div class="border-t border-amber-200 px-3 py-2">
								<p class="mb-1 text-xs text-amber-700">Desired (main) → Actual (running):</p>
								<ChangeList changes={driftChanges} />
								<div class="mt-3 flex items-center gap-2">
									<button
										onclick={adopt}
										disabled={reconciling}
										title="Stage the live state into a PR so git matches the cluster"
										class="rounded border border-amber-400 bg-white px-2.5 py-1 text-xs font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50"
									>
										Adopt into PR (running→main)
									</button>
									<button
										onclick={resync}
										disabled={reconciling}
										title="Trigger ArgoCD to reconcile the cluster back to git"
										class="rounded border border-amber-400 bg-white px-2.5 py-1 text-xs font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50"
									>
										Re-sync from git (main→running)
									</button>
								</div>
								{#if reconcileMsg}
									<p class="mt-2 text-xs {reconcileOk ? 'text-slate-600' : 'text-red-700'}">
										{reconcileMsg}
									</p>
								{/if}
							</div>
						{/if}
					</div>
				{/if}
			{:else if tab === 'monitor'}
				<!-- Monitor sub-rail: events + performance, vCenter's time-series home. -->
				<div class="mb-3 flex gap-1 border-b border-slate-200 text-sm">
					{#each ['events', 'performance'] as const as v (v)}
						<button
							class="border-b-2 px-3 py-1 capitalize {monitorView === v
								? 'border-blue-600 text-blue-700'
								: 'border-transparent text-slate-500 hover:text-slate-700'}"
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
				{:else if eventsLoading && !events}
					<div class="py-8 text-center text-sm text-slate-400">Loading events…</div>
				{:else if !events || events.length === 0}
					<div class="py-8 text-center text-sm text-slate-400">No recent events.</div>
				{:else}
					<table class="w-full text-[13px]">
						<thead class="text-left text-xs tracking-wide text-slate-400 uppercase">
							<tr class="border-b border-slate-200">
								<th class="py-1.5 pr-3 font-medium">Type</th>
								<th class="py-1.5 pr-3 font-medium">Reason</th>
								<th class="py-1.5 pr-3 font-medium">Message</th>
								<th class="py-1.5 pr-3 font-medium">Object</th>
								<th class="py-1.5 font-medium">Last seen</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100">
							{#each events as e, i (i)}
								<tr class={e.type === 'Warning' ? 'bg-amber-50/40' : ''}>
									<td class="py-1.5 pr-3">
										<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
											<span
												class="h-1.5 w-1.5 rounded-full {e.type === 'Warning'
													? 'bg-amber-500'
													: 'bg-slate-400'}"
											></span>
											{e.type}
										</span>
									</td>
									<td class="py-1.5 pr-3 font-medium text-slate-700">{e.reason}</td>
									<td class="py-1.5 pr-3 text-slate-600">{e.message}</td>
									<td class="py-1.5 pr-3 whitespace-nowrap text-slate-500">
										{e.object === 'VirtualMachineInstance' ? 'VMI' : 'VM'}
									</td>
									<td class="py-1.5 whitespace-nowrap text-slate-500">
										{duration(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-slate-400">
												×{e.count}</span
											>{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if tab === 'configure'}
				<VMConfigure {vm} {networks} onedit={openEdit} {onsearchlabel} />
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
		<CloneModal
			{vm}
			onclose={() => (cloning = false)}
			ondone={(ok) => onaction?.({ verb: 'Clone', namespace: vm.namespace, name: vm.name, ok })}
		/>
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
	<div class="flex h-full items-center justify-center text-sm text-slate-400">
		Select a VM from the inventory
	</div>
{/if}
