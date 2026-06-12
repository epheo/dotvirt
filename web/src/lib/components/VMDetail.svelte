<script lang="ts">
	import { Activity, ChevronDown, ChevronRight, Cpu, HardDrive, MemoryStick, Pencil, Trash2 } from 'lucide-svelte';
	import { api, type Change, type DraftItem, type VM, type VMEvent } from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import ActionMenu from './ActionMenu.svelte';
	import ChangeList from './ChangeList.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';
	import CapacityUsage from './CapacityUsage.svelte';
	import Console from './Console.svelte';
	import EditSettings from './EditSettings.svelte';
	import Performance from './Performance.svelte';
	import PowerDot from './PowerDot.svelte';
	import Snapshots from './Snapshots.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let {
		vm,
		onstaged,
		onaction,
		stagedItem = null,
		onstagedopen,
		onsearchlabel,
		intent = null
	}: {
		vm: VM | null;
		onstaged?: () => void;
		onaction?: (a: { verb: string; namespace: string; name: string; ok: boolean }) => void;
		stagedItem?: DraftItem | null;
		onstagedopen?: () => void;
		onsearchlabel?: (key: string, value: string) => void;
		// A one-shot request from outside (the context menu) to open a modal/tab
		// here; seq distinguishes repeated requests for the same id.
		intent?: { id: 'edit' | 'delete' | 'console' | 'snapshot'; seq: number } | null;
	} = $props();

	type Tab = 'summary' | 'monitor' | 'configure' | 'snapshots' | 'console';
	let tab = $state<Tab>('summary');
	// Monitor sub-rail (vCenter keeps all time-series under Monitor).
	let monitorView = $state<'events' | 'performance'>('events');
	// Configure sub-rail (vCenter's settings verb: read-only sections, each with
	// an Edit that stages a change through the PR flow).
	type ConfigSection = 'hardware' | 'storage' | 'network' | 'labels' | 'source';
	let configView = $state<ConfigSection>('hardware');
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
			.catch(() => (driftChanges = null));
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

	$effect(() => {
		// Reset when the selection changes, and (re)load drift for this VM.
		const cur = vm;
		tab = 'summary';
		monitorView = 'events';
		configView = 'hardware';
		editing = false;
		editSection = undefined;
		deleting = false;
		deleteErr = '';
		driftChanges = null;
		showDrift = false;
		reconcileMsg = '';
		events = null;
		eventsLoading = false;
		actionsOpen = false;
		runtimeMsg = '';
		if (cur) loadDrift(cur.namespace, cur.name);
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
			deleteErr = String(e);
		} finally {
			deleteBusy = false;
		}
	}

	// Elapsed time since an ISO timestamp, compact (e.g. "3d 21h") — VM uptime and
	// event age both use it.
	function elapsed(iso?: string): string {
		if (!iso) return '';
		const start = new Date(iso).getTime();
		if (Number.isNaN(start)) return '';
		let s = Math.max(0, Math.floor((Date.now() - start) / 1000));
		const d = Math.floor(s / 86400);
		const h = Math.floor((s % 86400) / 3600);
		const m = Math.floor((s % 3600) / 60);
		if (d > 0) return `${d}d ${h}h`;
		if (h > 0) return `${h}h ${m}m`;
		return `${m}m`;
	}
</script>

{#if vm}
	<div class="flex h-full flex-col">
		<div class="border-b border-slate-200 px-4 pt-4">
			<div class="mb-3 flex items-center gap-2">
				<PowerDot power={vm.power} paused={vm.paused} />
				<h2 class="text-lg font-semibold text-slate-800">{vm.name}</h2>
				<span class="rounded bg-slate-200 px-1.5 py-0.5 text-xs text-slate-600">{vm.namespace}</span>
				<SyncBadge sync={vm.sync} />
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
						title="Edit settings"
						class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50"
					>
						<Pencil size={13} /> Edit Settings
					</button>
					<button
						onclick={() => {
							deleting = true;
							deleteErr = '';
						}}
						title="Delete this VM (stages a removal into Changes)"
						class="flex items-center gap-1.5 rounded border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
					>
						<Trash2 size={13} /> Delete VM
					</button>
				</div>
			</div>
			<nav class="flex gap-1 text-sm">
				{#each ['summary', 'monitor', 'configure', 'snapshots', 'console'] as const as t (t)}
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
				{#snippet field(label: string, value: string)}
					<div class="flex justify-between gap-3 px-3 py-1.5">
						<dt class="shrink-0 text-slate-500">{label}</dt>
						<dd class="min-w-0 truncate text-right text-slate-800">{value || '—'}</dd>
					</div>
				{/snippet}

				<!-- At-a-glance tiles: the vCenter-style capacity summary. -->
				<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><Cpu size={13} /> CPU</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">
							{#if stagedChanges.has('CPU')}<span class="text-slate-400 line-through">{vm.cpuCores ?? '—'} vCPU</span> <span class="text-blue-600">{stagedChanges.get('CPU')?.to}</span>{:else}{vm.cpuCores ?? '—'}<span class="ml-1 text-sm font-normal text-slate-500">vCPU</span>{/if}
						</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><MemoryStick size={13} /> Memory</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">
							{#if stagedChanges.has('Memory')}<span class="text-slate-400 line-through">{vm.memory ?? '—'}</span> <span class="text-blue-600">{stagedChanges.get('Memory')?.to}</span>{:else}{vm.memory ?? '—'}{/if}
						</div>
						{#if vm.memoryActual && vm.memoryActual !== vm.memory}
							<div class="text-xs text-slate-400">{vm.memoryActual} live</div>
						{/if}
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><HardDrive size={13} /> Disks</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{vm.disks?.length ?? 0}</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><Activity size={13} /> Status</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{statusText}</div>
						{#if elapsed(vm.startedAt)}<div class="text-xs text-slate-400">up {elapsed(vm.startedAt)}</div>{/if}
					</div>
				</div>

				<!-- Live usage bars (vCenter "Capacity and Usage"), distinct from the
				     configuration tiles above. -->
				<div class="mt-4">
					<CapacityUsage {vm} />
				</div>

				<div class="mt-4 grid gap-4 md:grid-cols-2">
					<!-- Guest & runtime: live identity reported by the guest agent. -->
					<section class="rounded border border-slate-200">
						<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
							Guest &amp; runtime
						</h3>
						<dl class="divide-y divide-slate-100 text-[13px]">
							{@render field('Operating system', vm.os ?? '')}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">Power (desired)</dt>
								<dd class="min-w-0 text-right">
									{#if stagedChanges.has('Power')}<span class="text-slate-400 line-through">{vm.power}</span> <span class="text-blue-600">→ {stagedChanges.get('Power')?.to}</span>{:else}<span class="text-slate-800">{vm.power}</span>{/if}
								</dd>
							</div>
							{@render field('Status (actual)', vm.paused ? 'Paused' : (vm.phase ?? ''))}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">IP addresses</dt>
								<dd class="min-w-0 text-right font-mono text-xs text-slate-800">
									{#if vm.ips?.length}
										{#each vm.ips as ip (ip)}<div>{ip}</div>{/each}
									{:else}{vm.guestIP || '—'}{/if}
								</dd>
							</div>
						</dl>
					</section>

					<!-- Configuration & placement: desired config + where it runs. -->
					<section class="rounded border border-slate-200">
						<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
							Configuration &amp; placement
						</h3>
						<dl class="divide-y divide-slate-100 text-[13px]">
							{@render field('Instance type', vm.instancetype ?? '')}
							{@render field('Preference', vm.preference ?? '')}
							{@render field('Node', vm.nodeName ?? '')}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">Source</dt>
								<dd class="min-w-0 truncate text-right font-mono text-xs text-slate-600">{vm.sourceFile}</dd>
							</div>
						</dl>
					</section>
				</div>

				{#if driftChanges && driftChanges.length > 0}
					<div class="mt-4 rounded border border-amber-200 bg-amber-50">
						<button
							onclick={() => (showDrift = !showDrift)}
							class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium text-amber-800"
						>
							<span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>
							Drift — cluster differs from git ({driftChanges.length})
							<span class="ml-auto text-amber-600">{#if showDrift}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}</span>
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
						<Performance {vm} />
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
												class="h-1.5 w-1.5 rounded-full {e.type === 'Warning' ? 'bg-amber-500' : 'bg-slate-400'}"
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
										{elapsed(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-slate-400"> ×{e.count}</span
											>{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if tab === 'configure'}
				<!-- vCenter's settings verb: a left sub-rail of read-only sections; every
				     Edit stages a change through the PR flow (nothing writes the cluster). -->
				{#snippet cfgField(label: string, value: string)}
					<div class="flex justify-between gap-3 px-3 py-1.5">
						<dt class="shrink-0 text-slate-500">{label}</dt>
						<dd class="min-w-0 truncate text-right text-slate-800">{value || '—'}</dd>
					</div>
				{/snippet}
				{#snippet cfgHeader(title: string, section?: 'compute' | 'storage' | 'network' | 'labels')}
					<div class="flex items-center justify-between border-b border-slate-200 bg-slate-50 px-3 py-1.5">
						<h3 class="text-xs font-semibold tracking-wide text-slate-500 uppercase">{title}</h3>
						{#if section}
							<button
								onclick={() => openEdit(section)}
								class="flex items-center gap-1 text-xs text-blue-600 hover:underline"
							>
								<Pencil size={11} /> Edit
							</button>
						{/if}
					</div>
				{/snippet}
				<div class="flex gap-4">
					<nav class="w-36 shrink-0 text-[13px]">
						{#each [['hardware', 'VM Hardware'], ['storage', 'Storage'], ['network', 'Network'], ['labels', 'Labels'], ['source', 'Source & sync']] as const as [id, label] (id)}
							<button
								onclick={() => (configView = id)}
								class="block w-full rounded px-2.5 py-1.5 text-left {configView === id
									? 'bg-blue-50 font-medium text-blue-700'
									: 'text-slate-600 hover:bg-slate-50'}"
							>
								{label}
							</button>
						{/each}
					</nav>
					<div class="min-w-0 flex-1">
						{#if configView === 'hardware'}
							<section class="rounded border border-slate-200">
								{@render cfgHeader('VM Hardware', 'compute')}
								<dl class="divide-y divide-slate-100 text-[13px]">
									{@render cfgField('CPU cores', vm.cpuCores ? String(vm.cpuCores) : '')}
									{@render cfgField('Memory', vm.memory ?? '')}
									{@render cfgField('Instance type', vm.instancetype ?? '')}
									{@render cfgField('Preference', vm.preference ?? '')}
									{@render cfgField('Power (desired)', vm.power)}
								</dl>
							</section>
						{:else if configView === 'storage'}
							<section class="rounded border border-slate-200">
								{@render cfgHeader('Disks', 'storage')}
								{#if vm.disks?.length}
									<ul class="divide-y divide-slate-100 px-3 text-[13px]">
										{#each vm.disks as d (d.name)}
											<li class="flex justify-between gap-3 py-1.5">
												<span class="text-slate-800">{d.name}</span>
												<span class="text-slate-400">{d.type}{d.size ? ` · ${d.size}` : ''}</span>
											</li>
										{/each}
									</ul>
								{:else}
									<p class="px-3 py-3 text-xs text-slate-400">No disks defined in the manifest.</p>
								{/if}
							</section>
						{:else if configView === 'network'}
							<section class="rounded border border-slate-200">
								{@render cfgHeader('Network adapters', 'network')}
								{#if vm.networks?.length}
									<ul class="divide-y divide-slate-100 px-3 text-[13px]">
										{#each vm.networks as n (n.name)}
											<li class="flex justify-between gap-3 py-1.5">
												<span class="text-slate-800">{n.name}</span>
												<span class="text-slate-400">{n.network}</span>
											</li>
										{/each}
									</ul>
								{:else}
									<p class="px-3 py-3 text-xs text-slate-400">No adapters defined in the manifest.</p>
								{/if}
							</section>
						{:else if configView === 'labels'}
							<section class="rounded border border-slate-200">
								{@render cfgHeader('Labels', 'labels')}
								<div class="px-3 py-2">
									{#if vm.labels && Object.keys(vm.labels).length}
										{#each Object.entries(vm.labels) as [k, v] (k)}
											<button
												onclick={() => onsearchlabel?.(k, v)}
												title="Find everything labeled {k}={v}"
												class="mr-1 mb-1 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600 hover:bg-blue-50 hover:text-blue-700"
												>{k}={v}</button
											>
										{/each}
									{:else}
										<p class="py-1 text-xs text-slate-400">No labels.</p>
									{/if}
								</div>
							</section>
						{:else}
							<section class="rounded border border-slate-200">
								{@render cfgHeader('Source & sync')}
								<dl class="divide-y divide-slate-100 text-[13px]">
									<div class="flex justify-between gap-3 px-3 py-1.5">
										<dt class="shrink-0 text-slate-500">Manifest</dt>
										<dd class="min-w-0 truncate text-right font-mono text-xs text-slate-600">
											{vm.sourceFile}
										</dd>
									</div>
									{@render cfgField('Namespace', vm.namespace)}
									{@render cfgField('Sync', vm.sync)}
								</dl>
								<div class="border-t border-slate-100 px-3 py-2">
									<a
										href={manifestURL(vm)}
										target="_blank"
										class="text-xs text-blue-600 hover:underline">Download manifest ↗</a
									>
									<p class="mt-1 text-xs text-slate-400">
										This VM's configuration lives in git; edits become a pull request.
									</p>
								</div>
							</section>
						{/if}
					</div>
				</div>
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
			initialSection={editSection}
			onclose={() => (editing = false)}
			onstaged={() => onstaged?.()}
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
