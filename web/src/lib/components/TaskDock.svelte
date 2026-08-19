<script lang="ts">
	import { ChevronDown, ChevronUp, ListChecks, RefreshCw } from 'lucide-svelte';
	import {
		api,
		type Alert,
		type DraftView,
		type Inventory,
		type Proposal,
		type TaskEntry,
		type VMEvent,
	} from '$lib/api';
	import { resource } from '$lib/resource.svelte';
	import { buildTasks, type Task } from '$lib/tasks';
	import { persisted } from '$lib/state/persisted.svelte';
	import { severityTone, taskTone, TONE_TEXT } from '$lib/status';
	import { TBODY, THEAD_TR } from '$lib/table';
	import EventsTable from './EventsTable.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import StatusDot from './StatusDot.svelte';
	import TabBar from './TabBar.svelte';

	let {
		drafts,
		proposals,
		tasks: feed,
		inventory,
		username,
		onselect,
		onrefresh,
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		tasks: TaskEntry[];
		inventory: Inventory | null;
		username: string;
		onselect: (namespace: string, name: string) => void;
		onrefresh?: () => void;
	} = $props();

	type DockTab = 'tasks' | 'events' | 'alarms';

	let openPane = $state(true);
	let tab = $state<DockTab>('tasks');

	// Events lane: fetched on demand when the Events tab is opened (not on the
	// broadcast hot path), so a busy cluster's event churn can't spam the UI.
	let events = $state<VMEvent[] | null>(null);
	let eventsLoading = $state(false);

	// Firing Prometheus alerts for the Alarms tab. Polled slowly; the
	// read is one cached instant query server-side. null = endpoint unavailable
	// (metrics off), mapped from the failed flag.
	const alarmsRes = resource<Alert[]>(
		() => '',
		() => api.alarms(),
		{ poll: 30000 },
	);
	const firing = $derived(alarmsRes.failed ? null : alarmsRes.data);

	// Drag-to-resize the dock height. Persisted on release; restored clamped to
	// the current viewport so a height stored from a taller window can't swallow
	// the workspace.
	const dock = persisted('dotvirt.dock', { height: 192 });
	let dockHeight = $state(
		Math.max(80, Math.min(dock.value.height, (globalThis.innerHeight || 800) * 0.7)),
	);
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
		const next = dragStartH + (dragStartY - e.clientY); // drag up -> taller
		dockHeight = Math.max(80, Math.min(next, window.innerHeight * 0.7));
	}
	function onResizeEnd() {
		dragging = false;
		dock.value = { height: dockHeight };
	}

	function loadEvents() {
		eventsLoading = true;
		api
			.allEvents()
			.then((e) => (events = e))
			.catch(() => (events = []))
			.finally(() => (eventsLoading = false));
	}

	function selectTab(t: DockTab) {
		tab = t;
		openPane = true;
		if (t === 'events') loadEvents(); // refresh on each open
		if (t === 'alarms') alarmsRes.refresh();
	}

	const tasks = $derived(buildTasks({ inventory, feed, drafts, proposals, username }));

	// Drift + failed migrations come from the streamed inventory; firing
	// Prometheus alerts join them - one amber number for everything wrong.
	const clientAlarms = $derived(
		tasks.filter((t) => t.kind === 'drift' || (t.kind === 'migration' && !t.ok)),
	);
	const alarms = $derived(clientAlarms.length + (firing?.length ?? 0));

	// Both alarm sources projected into one row shape, so the table renders a
	// single {#each} whatever the origin.
	type AlarmRow = {
		key: string;
		name: string;
		count?: number;
		target: string;
		targetSub: string; // faint suffix ('' = none)
		severity: string;
		tone: ReturnType<typeof severityTone>;
		bg: string;
		source: 'Prometheus' | 'dotvirt';
		onclick: () => void;
	};
	const alarmRows = $derived.by((): AlarmRow[] => [
		...(firing ?? []).map((a): AlarmRow => ({
			key: `prom:${a.name}:${a.namespace ?? ''}/${a.vm ?? ''}:${a.severity ?? ''}`,
			name: a.name,
			count: (a.count ?? 0) > 1 ? a.count : undefined,
			target: a.vm || (a.namespace ?? '—'),
			targetSub: a.vm ? (a.namespace ?? '') : '',
			severity: a.severity ?? '—',
			tone: severityTone(a.severity),
			bg: 'bg-warn-soft/40',
			source: 'Prometheus',
			onclick: () => {
				if (a.namespace && a.vm) onselect(a.namespace, a.vm);
			},
		})),
		...clientAlarms.map((t): AlarmRow => ({
			key: `dotvirt:${t.kind}:${t.namespace}/${t.name}`,
			name: t.verb,
			target: t.name,
			targetSub: t.namespace,
			severity: t.kind === 'drift' ? 'warning' : 'critical',
			tone: taskTone(t),
			bg: t.kind === 'drift' ? 'bg-warn-soft/40' : 'bg-danger-soft/40',
			source: 'dotvirt',
			onclick: () => activate(t),
		})),
	]);

	const rowClass = (t: Task) =>
		t.kind === 'drift'
			? 'bg-warn-soft/40'
			: t.kind === 'migration' && !t.ok
				? 'bg-danger-soft/40'
				: t.kind === 'pr'
					? 'bg-ok-soft/30'
					: '';

	// Row click: open the PR for proposed rows, else focus the target VM's detail.
	function activate(t: Task) {
		if (t.url) window.open(t.url, '_blank', 'noopener');
		else onselect(t.namespace, t.name);
	}
</script>

{#snippet dockHead(cols: string[])}
	<!-- The one header row the tasks and alarms tables share. -->
	<thead class="sticky top-0 bg-inset text-left text-[11px] tracking-wide text-ink-faint uppercase">
		<tr class={THEAD_TR}>
			{#each cols as c (c)}
				<th class="px-3 py-1.5 font-medium">{c}</th>
			{/each}
		</tr>
	</thead>
{/snippet}

<section class="border-t border-line-strong bg-panel text-xs">
	{#if openPane}
		<!-- Drag the top edge to resize the dock. -->
		<div
			class="h-1.5 w-full cursor-ns-resize bg-inset-strong hover:bg-accent/40"
			onpointerdown={onResizeStart}
			onpointermove={onResizeMove}
			onpointerup={onResizeEnd}
			role="separator"
			aria-orientation="horizontal"
			aria-label="Resize panel"
		></div>
	{/if}
	<!-- Tabbed header: Recent Tasks | Events + collapse. -->
	<div class="flex items-center gap-1 bg-inset-strong px-2 py-1 text-ink-soft">
		<ListChecks size={14} class="mx-1 text-ink-muted" />
		<TabBar
			tabs={[
				{ id: 'tasks', label: 'Recent Tasks', count: tasks.length },
				{ id: 'events', label: 'Events' },
				{
					id: 'alarms',
					label: 'Alarms',
					count: alarms > 0 ? alarms : undefined,
					countTone: 'warn',
				},
			]}
			active={openPane ? tab : ''}
			variant="chips"
			onchange={(t) => selectTab(t as DockTab)}
		/>
		<button
			onclick={() => {
				onrefresh?.();
				if (tab === 'events') loadEvents();
			}}
			class="ml-auto p-1 text-ink-faint hover:text-ink-soft"
			title="Refresh"
		>
			<RefreshCw size={13} />
		</button>
		<button
			onclick={() => (openPane = !openPane)}
			class="p-1 text-ink-faint hover:text-ink-soft"
			title="Collapse/expand"
		>
			{#if openPane}<ChevronDown size={14} />{:else}<ChevronUp size={14} />{/if}
		</button>
	</div>

	{#if openPane}
		<div class="overflow-y-auto" style="height: {dockHeight}px">
			{#if tab === 'tasks'}
				{#if tasks.length === 0}
					<div class="px-3 py-5 text-center text-ink-faint">No active tasks.</div>
				{:else}
					<table class="w-full">
						{@render dockHead(['Task', 'Target', 'Status', 'Initiated by', 'Project'])}
						<tbody class={TBODY}>
							{#each tasks as t (t.kind + ':' + t.project + ':' + t.namespace + '/' + t.name + ':' + t.url + ':' + (t.at ?? ''))}
								<tr
									onclick={() => activate(t)}
									class="cursor-pointer hover:bg-select-soft {rowClass(t)}"
								>
									<td class="px-3 py-1.5 text-ink-soft">
										<button
											onclick={(e) => {
												e.stopPropagation();
												activate(t);
											}}
											class="hover:underline focus-visible:underline">{t.verb}</button
										>
									</td>
									<td class="px-3 py-1.5 font-medium text-ink">
										{#if t.kind === 'pr' || t.kind === 'sync'}
											<span class="font-normal text-ink-soft">{t.prTitle}</span>
										{:else}
											{t.name} <span class="font-normal text-ink-faint">· {t.namespace}</span>
										{/if}
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<StatusDot tone={taskTone(t)} size="xs" pulse={!!t.active} />
											<span class={TONE_TEXT[taskTone(t)]}>{t.status}</span>
											{#if t.kind === 'staged'}
												<GitOpsStepper stage="staged" compact />
											{:else if t.kind === 'pr'}
												<GitOpsStepper stage="proposed" compact />
											{:else if t.kind === 'sync'}
												<GitOpsStepper stage={t.active ? 'merged' : 'synced'} compact />
											{/if}
										</span>
									</td>
									<td class="px-3 py-1.5 text-ink-soft">{t.by}</td>
									<td class="px-3 py-1.5 text-ink-muted">{t.project}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if tab === 'alarms'}
				<!-- The Alarms tab: firing Prometheus alerts + the
				     inventory-derived amber set (drift, failed migrations). -->
				{#if alarms === 0}
					<div class="px-3 py-5 text-center text-ink-faint">
						No triggered alarms{firing === null ? ' (alerts feed unavailable)' : ''}.
					</div>
				{:else}
					<table class="w-full">
						{@render dockHead(['Alarm', 'Target', 'Severity', 'Source'])}
						<tbody class={TBODY}>
							{#each alarmRows as r (r.key)}
								<tr onclick={r.onclick} class="cursor-pointer {r.bg} hover:bg-select-soft">
									<td class="px-3 py-1.5 font-medium text-ink-soft">
										<button
											onclick={(e) => {
												e.stopPropagation();
												r.onclick();
											}}
											class="hover:underline focus-visible:underline">{r.name}</button
										>{#if r.count}<span class="text-ink-faint"> ×{r.count}</span>{/if}
									</td>
									<td class="px-3 py-1.5 text-ink">
										{r.target}{#if r.targetSub}
											<span class="text-ink-faint">· {r.targetSub}</span>{/if}
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<StatusDot tone={r.tone} size="xs" />
											{r.severity}
										</span>
									</td>
									<td class="px-3 py-1.5 text-ink-muted">{r.source}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else}
				<div class="px-3 py-2">
					<EventsTable {events} loading={eventsLoading} showVM {onselect} />
				</div>
			{/if}
		</div>
	{/if}
</section>
