<script lang="ts">
	import { ChevronDown, ChevronUp, ListChecks, RefreshCw } from '@lucide/svelte';
	import {
		api,
		type DraftView,
		type Inventory,
		type Proposal,
		type TaskEntry,
		type VMEvent,
	} from '#lib/api.js';
	import { resource } from '#lib/resource.svelte.js';
	import { buildTasks, type Task } from '#lib/tasks.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { taskTone, TONE_TEXT } from '#lib/status.js';
	import { TBODY, THEAD_TR } from '#lib/table.js';
	import Button from './Button.svelte';
	import EventsTable from './EventsTable.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import Note from './Note.svelte';
	import StatusDot from './StatusDot.svelte';
	import TabBar from './TabBar.svelte';

	let {
		drafts,
		proposals,
		tasks: feed,
		tasksError = '',
		inventory,
		username,
		onselect,
		onrefresh,
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		tasks: TaskEntry[];
		tasksError?: string; // the feed pull failed; onrefresh retries it
		inventory: Inventory | null;
		username: string;
		onselect: (namespace: string, name: string) => void;
		onrefresh?: () => void;
	} = $props();

	type DockTab = 'tasks' | 'events';

	// Collapsed on every load: the rail is ambient, the pane is on demand and
	// never comes back open by itself. Only its height persists.
	let openPane = $state(false);
	let tab = $state<DockTab>('tasks');

	// Events lane: pulled only while the Events tab is open (never on the
	// broadcast hot path), so a busy cluster's event churn can't spam the UI.
	// Every open is a key change, so it re-pulls; closed resolves empty.
	const eventsRes = resource<VMEvent[]>(
		() => (openPane && tab === 'events' ? 'open' : ''),
		(k) => (k ? api.allEvents() : Promise.resolve([])),
		{ reset: true },
	);

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

	function selectTab(t: DockTab) {
		tab = t;
		openPane = true;
	}

	const tasks = $derived(buildTasks({ inventory, feed, drafts, proposals, username }));

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
	<!-- The task table's header row. -->
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
			aria-label="Resize Recent Tasks"
		></div>
	{/if}
	<!-- Tabbed header: Recent Tasks | Events + collapse. -->
	<div class="flex items-center gap-1 bg-inset-strong px-2 py-1 text-ink-soft">
		<ListChecks size={14} class="mx-1 text-ink-muted" />
		<TabBar
			tabs={[
				{ id: 'tasks', label: 'Recent Tasks', count: tasks.length },
				{ id: 'events', label: 'Events' },
			]}
			active={openPane ? tab : ''}
			variant="chips"
			onchange={(t) => selectTab(t as DockTab)}
		/>
		{#if !openPane && tasks[0]}
			{@const t = tasks[0]}
			<button
				onclick={() => selectTab('tasks')}
				class="flex min-w-0 items-center gap-1.5 px-2 text-ink-muted hover:text-ink-soft"
				title="Open Recent Tasks"
			>
				<StatusDot tone={taskTone(t)} size="xs" pulse={t.kind === 'migration' && t.active} />
				<span class="truncate">{t.verb} {t.name || t.prTitle || t.project}</span>
				<span class="shrink-0">{t.status}</span>
			</button>
		{/if}
		<button
			onclick={() => {
				onrefresh?.();
				if (tab === 'events') eventsRes.refresh();
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
				{#if tasksError}
					<Note tone="danger" class="m-2 flex items-center gap-2">
						<span class="min-w-0 flex-1">Recent tasks could not be read: {tasksError}</span>
						<Button variant="link" size="sm" onclick={() => onrefresh?.()}>Retry</Button>
					</Note>
				{/if}
				{#if tasks.length === 0}
					{#if !tasksError}
						<div class="px-3 py-5 text-center text-ink-faint">No active tasks.</div>
					{/if}
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
			{:else}
				<div class="px-3 py-2">
					<EventsTable
						events={eventsRes.data}
						loading={eventsRes.loading}
						error={eventsRes.error}
						onretry={eventsRes.refresh}
						showVM
						{onselect}
					/>
				</div>
			{/if}
		</div>
	{/if}
</section>
