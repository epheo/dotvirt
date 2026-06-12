<script lang="ts">
	import { ChevronDown, ChevronUp, ListChecks, RefreshCw } from 'lucide-svelte';
	import {
		api,
		type Alert,
		type DraftView,
		type Inventory,
		type Proposal,
		type VMEvent
	} from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';

	let {
		drafts,
		proposals,
		actions,
		inventory,
		username,
		onselect,
		onrefresh
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		actions: { verb: string; namespace: string; name: string; ok: boolean; at: number }[];
		inventory: Inventory | null;
		username: string;
		onselect: (namespace: string, name: string) => void;
		onrefresh?: () => void;
	} = $props();

	let openPane = $state(true);
	let tab = $state<'tasks' | 'events' | 'alarms'>('tasks');

	// Events lane: fetched on demand when the Events tab is opened (not on the
	// broadcast hot path), so a busy cluster's event churn can't spam the UI.
	let events = $state<VMEvent[] | null>(null);
	let eventsLoading = $state(false);

	// Firing Prometheus alerts (vCenter's Triggered Alarms). Polled slowly even
	// with the tab closed so the header badge stays honest; the read is one
	// cached instant query server-side. null = endpoint unavailable (metrics off).
	let firing = $state<Alert[] | null>(null);
	function loadAlarms() {
		api
			.alarms()
			.then((a) => (firing = a))
			.catch(() => (firing = null));
	}
	$effect(() => pollWhileVisible(loadAlarms, 30000));

	// Drag-to-resize the dock height (the fixed height was cramped for many rows).
	let dockHeight = $state(192);
	let dragging = false;
	let dragStartY = 0;
	let dragStartH = 0;
	function onResizeStart(e: PointerEvent) {
		dragging = true;
		dragStartY = e.clientY;
		dragStartH = dockHeight;
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
	}
	function onResizeMove(e: PointerEvent) {
		if (!dragging) return;
		const next = dragStartH + (dragStartY - e.clientY); // drag up → taller
		dockHeight = Math.max(80, Math.min(next, window.innerHeight * 0.7));
	}
	function onResizeEnd() {
		dragging = false;
	}

	function loadEvents() {
		eventsLoading = true;
		api
			.allEvents()
			.then((e) => (events = e))
			.catch(() => (events = []))
			.finally(() => (eventsLoading = false));
	}

	function selectTab(t: 'tasks' | 'events' | 'alarms') {
		tab = t;
		openPane = true;
		if (t === 'events') loadEvents(); // refresh on each open
		if (t === 'alarms') loadAlarms();
	}

	type Task = {
		kind: 'staged' | 'pr' | 'drift' | 'action' | 'migration';
		verb: string;
		namespace: string;
		name: string;
		prTitle: string;
		status: string;
		by: string;
		project: string;
		url: string;
		ok?: boolean; // for 'action'/'migration' rows: success
		at?: number; // for 'action' rows: timestamp (keeps keys unique)
		active?: boolean; // for 'migration' rows: still moving
	};

	// One unified feed ordered by lifecycle stage, not timestamp: live migrations →
	// runtime ops → staged changes (the draft) → open PRs (proposed) → standing
	// drift (cluster ≠ git).
	const tasks = $derived.by<Task[]>(() => {
		const out: Task[] = [];
		// Live node-to-node moves (vCenter's vMotion rows) — streamed off the VMI's
		// migration state; finished ones linger for a short window.
		const migrationLingerMs = 15 * 60 * 1000;
		if (inventory) {
			for (const proj of inventory.projects)
				for (const ns of proj.namespaces)
					for (const vm of ns.vms) {
						const m = vm.migration;
						if (!m) continue;
						const active = !m.completed && !m.failed;
						const endedRecently = m.endedAt
							? Date.now() - new Date(m.endedAt).getTime() < migrationLingerMs
							: false;
						if (!active && !endedRecently) continue;
						out.push({
							kind: 'migration',
							verb: 'Live-migration',
							namespace: vm.namespace,
							name: vm.name,
							prTitle: '',
							status: active
								? `${m.sourceNode ?? '?'} → ${m.targetNode ?? '?'}${m.startedAt ? ` · ${age(m.startedAt)}` : ''}`
								: m.failed
									? 'Failed'
									: `Migrated to ${m.targetNode ?? '?'}`,
							by: '—',
							project: proj.name,
							url: '',
							ok: !m.failed,
							active
						});
					}
		}
		// Imperative runtime ops the user just triggered (most recent first).
		for (const a of actions) {
			out.push({
				kind: 'action',
				verb: a.verb,
				namespace: a.namespace,
				name: a.name,
				prTitle: '',
				status: a.ok ? 'Requested' : 'Failed',
				by: username,
				project: '',
				url: '',
				ok: a.ok,
				at: a.at
			});
		}
		for (const { project, draft } of drafts) {
			for (const it of draft.items) {
				out.push({
					kind: 'staged',
					verb: it.kind === 'edit' ? 'Reconfigure' : it.kind === 'create' ? 'Create' : 'Delete',
					namespace: it.namespace,
					name: it.name,
					prTitle: '',
					status: 'Staged',
					by: username,
					project,
					url: ''
				});
			}
		}
		for (const p of proposals) {
			out.push({
				kind: 'pr',
				verb: 'Proposed',
				namespace: '',
				name: '',
				prTitle: p.title || `PR #${p.prNumber}`,
				status: `PR #${p.prNumber} open`,
				by: username,
				project: p.project,
				url: p.prURL
			});
		}
		if (inventory) {
			for (const proj of inventory.projects)
				for (const ns of proj.namespaces)
					for (const vm of ns.vms)
						if (vm.sync === 'OutOfSync')
							out.push({
								kind: 'drift',
								verb: 'Configuration drift',
								namespace: vm.namespace,
								name: vm.name,
								prTitle: '',
								status: 'Drifted',
								by: '—',
								project: proj.name,
								url: ''
							});
		}
		return out;
	});

	// Drift + failed migrations come from the streamed inventory; firing
	// Prometheus alerts join them — one amber number for everything wrong.
	const clientAlarms = $derived(
		tasks.filter((t) => t.kind === 'drift' || (t.kind === 'migration' && !t.ok))
	);
	const alarms = $derived(clientAlarms.length + (firing?.length ?? 0));

	const severityClass = (s?: string) =>
		s === 'critical' ? 'bg-red-500' : s === 'warning' ? 'bg-amber-500' : 'bg-slate-400';

	const dotClass = (t: Task) =>
		t.kind === 'drift'
			? 'bg-amber-500'
			: t.kind === 'migration'
				? t.active
					? 'animate-pulse bg-blue-500'
					: t.ok
						? 'bg-emerald-500'
						: 'bg-red-500'
				: t.kind === 'pr'
					? 'bg-emerald-500'
					: t.kind === 'action'
						? t.ok
							? 'bg-emerald-500'
							: 'bg-red-500'
						: 'bg-blue-500';
	const textClass = (t: Task) =>
		t.kind === 'drift'
			? 'text-amber-700'
			: t.kind === 'migration'
				? t.active
					? 'text-blue-700'
					: t.ok
						? 'text-emerald-700'
						: 'text-red-700'
				: t.kind === 'pr'
					? 'text-emerald-700'
					: t.kind === 'action'
						? t.ok
							? 'text-emerald-700'
							: 'text-red-700'
						: 'text-slate-600';
	const rowClass = (t: Task) =>
		t.kind === 'drift'
			? 'bg-amber-50/40'
			: t.kind === 'migration' && !t.ok
				? 'bg-red-50/40'
				: t.kind === 'pr'
					? 'bg-emerald-50/30'
					: '';

	// Row click: open the PR for proposed rows, else focus the target VM's detail.
	function activate(t: Task) {
		if (t.url) window.open(t.url, '_blank', 'noopener');
		else onselect(t.namespace, t.name);
	}

	// Compact age for events (sub-minute matters — events can be seconds old).
	function age(iso?: string): string {
		if (!iso) return '';
		const start = new Date(iso).getTime();
		if (Number.isNaN(start)) return '';
		const s = Math.max(0, Math.floor((Date.now() - start) / 1000));
		const d = Math.floor(s / 86400);
		const h = Math.floor((s % 86400) / 3600);
		const m = Math.floor((s % 3600) / 60);
		if (d > 0) return `${d}d ${h}h`;
		if (h > 0) return `${h}h ${m}m`;
		if (m > 0) return `${m}m`;
		return `${s}s`;
	}
</script>

<section class="border-t border-slate-300 bg-white text-xs">
	{#if openPane}
		<!-- Drag the top edge to resize the dock. -->
		<div
			class="h-1.5 w-full cursor-ns-resize bg-slate-100 hover:bg-blue-300"
			onpointerdown={onResizeStart}
			onpointermove={onResizeMove}
			onpointerup={onResizeEnd}
			role="separator"
			aria-orientation="horizontal"
			aria-label="Resize panel"
		></div>
	{/if}
	<!-- Tabbed header (vCenter's bottom pane): Recent Tasks | Events + collapse. -->
	<div class="flex items-center gap-1 bg-slate-100 px-2 py-1 text-slate-600">
		<ListChecks size={14} class="mx-1 text-slate-500" />
		<button
			onclick={() => selectTab('tasks')}
			class="rounded px-2 py-0.5 font-semibold tracking-wide uppercase {tab === 'tasks' && openPane
				? 'bg-white text-slate-700 shadow-sm'
				: 'text-slate-500 hover:text-slate-700'}"
		>
			Recent Tasks
			<span class="ml-0.5 rounded-full bg-slate-300 px-1.5 text-[11px] text-slate-700">{tasks.length}</span>
		</button>
		<button
			onclick={() => selectTab('events')}
			class="rounded px-2 py-0.5 font-semibold tracking-wide uppercase {tab === 'events' && openPane
				? 'bg-white text-slate-700 shadow-sm'
				: 'text-slate-500 hover:text-slate-700'}"
		>
			Events
		</button>
		<button
			onclick={() => selectTab('alarms')}
			class="rounded px-2 py-0.5 font-semibold tracking-wide uppercase {tab === 'alarms' && openPane
				? 'bg-white text-slate-700 shadow-sm'
				: 'text-slate-500 hover:text-slate-700'}"
		>
			Alarms
			{#if alarms > 0}
				<span class="ml-0.5 rounded-full bg-amber-200 px-1.5 text-[11px] font-medium text-amber-800"
					>{alarms}</span
				>
			{/if}
		</button>
		<button
			onclick={() => {
				onrefresh?.();
				if (tab === 'events') loadEvents();
			}}
			class="ml-auto p-1 text-slate-400 hover:text-slate-600"
			title="Refresh"
		>
			<RefreshCw size={13} />
		</button>
		<button
			onclick={() => (openPane = !openPane)}
			class="p-1 text-slate-400 hover:text-slate-600"
			title="Collapse/expand"
		>
			{#if openPane}<ChevronDown size={14} />{:else}<ChevronUp size={14} />{/if}
		</button>
	</div>

	{#if openPane}
		<div class="overflow-y-auto" style="height: {dockHeight}px">
			{#if tab === 'tasks'}
				{#if tasks.length === 0}
					<div class="px-3 py-5 text-center text-slate-400">No active tasks.</div>
				{:else}
					<table class="w-full">
						<thead
							class="sticky top-0 bg-slate-50 text-left text-[11px] tracking-wide text-slate-400 uppercase"
						>
							<tr class="border-b border-slate-200">
								<th class="px-3 py-1.5 font-medium">Task</th>
								<th class="px-3 py-1.5 font-medium">Target</th>
								<th class="px-3 py-1.5 font-medium">Status</th>
								<th class="px-3 py-1.5 font-medium">Initiated by</th>
								<th class="px-3 py-1.5 font-medium">Project</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100">
							{#each tasks as t (t.kind + ':' + t.project + ':' + t.namespace + '/' + t.name + ':' + t.url + ':' + (t.at ?? ''))}
								<tr onclick={() => activate(t)} class="cursor-pointer hover:bg-blue-50 {rowClass(t)}">
									<td class="px-3 py-1.5 text-slate-700">{t.verb}</td>
									<td class="px-3 py-1.5 font-medium text-slate-800">
										{#if t.kind === 'pr'}
											<span class="font-normal text-slate-700">{t.prTitle}</span>
										{:else}
											{t.name} <span class="font-normal text-slate-400">· {t.namespace}</span>
										{/if}
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<span class="h-1.5 w-1.5 rounded-full {dotClass(t)}"></span>
											<span class={textClass(t)}>{t.status}</span>
										</span>
									</td>
									<td class="px-3 py-1.5 text-slate-600">{t.by}</td>
									<td class="px-3 py-1.5 text-slate-500">{t.project}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if tab === 'alarms'}
				<!-- vCenter's Triggered Alarms: firing Prometheus alerts + the
				     inventory-derived amber set (drift, failed migrations). -->
				{#if alarms === 0}
					<div class="px-3 py-5 text-center text-slate-400">
						No triggered alarms{firing === null ? ' (alerts feed unavailable)' : ''}.
					</div>
				{:else}
					<table class="w-full">
						<thead
							class="sticky top-0 bg-slate-50 text-left text-[11px] tracking-wide text-slate-400 uppercase"
						>
							<tr class="border-b border-slate-200">
								<th class="px-3 py-1.5 font-medium">Alarm</th>
								<th class="px-3 py-1.5 font-medium">Target</th>
								<th class="px-3 py-1.5 font-medium">Severity</th>
								<th class="px-3 py-1.5 font-medium">Source</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100">
							{#each firing ?? [] as a (a.name + ':' + (a.namespace ?? '') + '/' + (a.vm ?? '') + ':' + (a.severity ?? ''))}
								<tr
									onclick={() => a.namespace && a.vm && onselect(a.namespace, a.vm)}
									class="cursor-pointer bg-amber-50/40 hover:bg-blue-50"
								>
									<td class="px-3 py-1.5 font-medium text-slate-700">
										{a.name}{#if (a.count ?? 0) > 1}<span class="text-slate-400"> ×{a.count}</span>{/if}
									</td>
									<td class="px-3 py-1.5 text-slate-800">
										{#if a.vm}{a.vm} <span class="text-slate-400">· {a.namespace}</span>
										{:else}{a.namespace ?? '—'}{/if}
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<span class="h-1.5 w-1.5 rounded-full {severityClass(a.severity)}"></span>
											{a.severity ?? '—'}
										</span>
									</td>
									<td class="px-3 py-1.5 text-slate-500">Prometheus</td>
								</tr>
							{/each}
							{#each clientAlarms as t (t.kind + ':' + t.namespace + '/' + t.name)}
								<tr
									onclick={() => activate(t)}
									class="cursor-pointer {t.kind === 'drift'
										? 'bg-amber-50/40'
										: 'bg-red-50/40'} hover:bg-blue-50"
								>
									<td class="px-3 py-1.5 font-medium text-slate-700">{t.verb}</td>
									<td class="px-3 py-1.5 text-slate-800">
										{t.name} <span class="text-slate-400">· {t.namespace}</span>
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<span
												class="h-1.5 w-1.5 rounded-full {t.kind === 'drift'
													? 'bg-amber-500'
													: 'bg-red-500'}"
											></span>
											{t.kind === 'drift' ? 'warning' : 'critical'}
										</span>
									</td>
									<td class="px-3 py-1.5 text-slate-500">dotvirt</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if eventsLoading && events === null}
				<div class="px-3 py-5 text-center text-slate-400">Loading events…</div>
			{:else if !events || events.length === 0}
				<div class="px-3 py-5 text-center text-slate-400">No recent events.</div>
			{:else}
				<table class="w-full">
					<thead
						class="sticky top-0 bg-slate-50 text-left text-[11px] tracking-wide text-slate-400 uppercase"
					>
						<tr class="border-b border-slate-200">
							<th class="px-3 py-1.5 font-medium">Reason</th>
							<th class="px-3 py-1.5 font-medium">Target</th>
							<th class="px-3 py-1.5 font-medium">Message</th>
							<th class="px-3 py-1.5 font-medium">Type</th>
							<th class="px-3 py-1.5 font-medium">Last seen</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100">
						{#each events as e, i (i)}
							<tr
								onclick={() => e.namespace && e.name && onselect(e.namespace, e.name)}
								class="cursor-pointer hover:bg-blue-50 {e.type === 'Warning' ? 'bg-amber-50/40' : ''}"
							>
								<td class="px-3 py-1.5 font-medium text-slate-700">{e.reason}</td>
								<td class="px-3 py-1.5 text-slate-800">
									{e.name} <span class="text-slate-400">· {e.namespace}</span>
								</td>
								<td class="px-3 py-1.5 text-slate-600">{e.message}</td>
								<td class="px-3 py-1.5">
									<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
										<span
											class="h-1.5 w-1.5 rounded-full {e.type === 'Warning' ? 'bg-amber-500' : 'bg-slate-400'}"
										></span>
										{e.type}
									</span>
								</td>
								<td class="px-3 py-1.5 whitespace-nowrap text-slate-500">
									{age(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-slate-400"> ×{e.count}</span
										>{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/if}
</section>
