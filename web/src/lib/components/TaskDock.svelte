<script lang="ts">
	import { untrack } from 'svelte';
	import { ChevronDown, ChevronUp, ListChecks, RefreshCw } from 'lucide-svelte';
	import {
		api,
		type Alert,
		type DraftView,
		type Inventory,
		type Proposal,
		type VMEvent,
	} from '$lib/api';
	import { duration } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';
	import { persisted } from '$lib/state/persisted.svelte';
	import { severityTone, taskTone, TONE_TEXT } from '$lib/status';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import StatusDot from './StatusDot.svelte';
	import TabBar from './TabBar.svelte';

	let {
		drafts,
		proposals,
		actions,
		inventory,
		username,
		onselect,
		onrefresh,
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
		const next = dragStartH + (dragStartY - e.clientY); // drag up → taller
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

	function selectTab(t: 'tasks' | 'events' | 'alarms') {
		tab = t;
		openPane = true;
		if (t === 'events') loadEvents(); // refresh on each open
		if (t === 'alarms') loadAlarms();
	}

	// A PR lane vanishing from the live stream means it merged (or closed); while
	// the project still shows OutOfSync VMs, surface it as "ArgoCD syncing" — the
	// merge→reconcile gap an admin otherwise can't see. Best-effort and purely
	// client-derived; entries expire after a linger window.
	const SYNC_LINGER_MS = 5 * 60 * 1000;
	let merged = $state<{ project: string; prNumber: number; title: string; at: number }[]>([]);
	let prevProposals: Proposal[] = [];
	$effect(() => {
		const cur = proposals;
		untrack(() => {
			const keys = new Set(cur.map((p) => `${p.project}#${p.prNumber}`));
			const gone = prevProposals.filter((p) => !keys.has(`${p.project}#${p.prNumber}`));
			if (gone.length)
				merged = [
					...merged,
					...gone.map((p) => ({
						project: p.project,
						prNumber: p.prNumber,
						title: p.title ?? '',
						at: Date.now(),
					})),
				].slice(-20);
			prevProposals = cur;
		});
	});

	type Task = {
		kind: 'staged' | 'pr' | 'sync' | 'drift' | 'action' | 'migration';
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
								? `${m.sourceNode ?? '?'} → ${m.targetNode ?? '?'}${m.startedAt ? ` · ${duration(m.startedAt)}` : ''}`
								: m.failed
									? 'Failed'
									: `Migrated to ${m.targetNode ?? '?'}`,
							by: '—',
							project: proj.name,
							url: '',
							ok: !m.failed,
							active,
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
				at: a.at,
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
					url: '',
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
				url: p.prURL,
			});
		}
		// Freshly merged lanes: "syncing" while their project still drifts.
		for (const m of merged) {
			if (Date.now() - m.at > SYNC_LINGER_MS) continue;
			const drifting = inventory?.projects.some(
				(p) =>
					p.name === m.project &&
					p.namespaces.some((ns) => ns.vms.some((v) => v.sync === 'OutOfSync')),
			);
			out.push({
				kind: 'sync',
				verb: 'Merged',
				namespace: '',
				name: '',
				prTitle: m.title || `PR #${m.prNumber}`,
				status: drifting ? 'ArgoCD syncing…' : 'Synced',
				by: username,
				project: m.project,
				url: '',
				ok: true,
				active: !!drifting,
				at: m.at,
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
								url: '',
							});
		}
		return out;
	});

	// Drift + failed migrations come from the streamed inventory; firing
	// Prometheus alerts join them — one amber number for everything wrong.
	const clientAlarms = $derived(
		tasks.filter((t) => t.kind === 'drift' || (t.kind === 'migration' && !t.ok)),
	);
	const alarms = $derived(clientAlarms.length + (firing?.length ?? 0));

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
	<!-- The one header row all three dock tables share. -->
	<thead
		class="sticky top-0 bg-inset text-left text-[11px] tracking-wide text-ink-faint uppercase"
	>
		<tr class="border-b border-line">
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
	<!-- Tabbed header (vCenter's bottom pane): Recent Tasks | Events + collapse. -->
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
			onchange={(t) => selectTab(t as 'tasks' | 'events' | 'alarms')}
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
						<tbody class="divide-y divide-line-soft">
							{#each tasks as t (t.kind + ':' + t.project + ':' + t.namespace + '/' + t.name + ':' + t.url + ':' + (t.at ?? ''))}
								<tr
									onclick={() => activate(t)}
									class="cursor-pointer hover:bg-select-soft {rowClass(t)}"
								>
									<td class="px-3 py-1.5 text-ink-soft">{t.verb}</td>
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
				<!-- vCenter's Triggered Alarms: firing Prometheus alerts + the
				     inventory-derived amber set (drift, failed migrations). -->
				{#if alarms === 0}
					<div class="px-3 py-5 text-center text-ink-faint">
						No triggered alarms{firing === null ? ' (alerts feed unavailable)' : ''}.
					</div>
				{:else}
					<table class="w-full">
						{@render dockHead(['Alarm', 'Target', 'Severity', 'Source'])}
						<tbody class="divide-y divide-line-soft">
							{#each firing ?? [] as a (a.name + ':' + (a.namespace ?? '') + '/' + (a.vm ?? '') + ':' + (a.severity ?? ''))}
								<tr
									onclick={() => a.namespace && a.vm && onselect(a.namespace, a.vm)}
									class="cursor-pointer bg-warn-soft/40 hover:bg-select-soft"
								>
									<td class="px-3 py-1.5 font-medium text-ink-soft">
										{a.name}{#if (a.count ?? 0) > 1}<span class="text-ink-faint">
												×{a.count}</span
											>{/if}
									</td>
									<td class="px-3 py-1.5 text-ink">
										{#if a.vm}{a.vm} <span class="text-ink-faint">· {a.namespace}</span>
										{:else}{a.namespace ?? '—'}{/if}
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<StatusDot tone={severityTone(a.severity)} size="xs" />
											{a.severity ?? '—'}
										</span>
									</td>
									<td class="px-3 py-1.5 text-ink-muted">Prometheus</td>
								</tr>
							{/each}
							{#each clientAlarms as t (t.kind + ':' + t.namespace + '/' + t.name)}
								<tr
									onclick={() => activate(t)}
									class="cursor-pointer {t.kind === 'drift'
										? 'bg-warn-soft/40'
										: 'bg-danger-soft/40'} hover:bg-select-soft"
								>
									<td class="px-3 py-1.5 font-medium text-ink-soft">{t.verb}</td>
									<td class="px-3 py-1.5 text-ink">
										{t.name} <span class="text-ink-faint">· {t.namespace}</span>
									</td>
									<td class="px-3 py-1.5">
										<span class="inline-flex items-center gap-1.5">
											<StatusDot tone={taskTone(t)} size="xs" />
											{t.kind === 'drift' ? 'warning' : 'critical'}
										</span>
									</td>
									<td class="px-3 py-1.5 text-ink-muted">dotvirt</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{:else if eventsLoading && events === null}
				<div class="px-3 py-5 text-center text-ink-faint">Loading events…</div>
			{:else if !events || events.length === 0}
				<div class="px-3 py-5 text-center text-ink-faint">No recent events.</div>
			{:else}
				<table class="w-full">
					{@render dockHead(['Reason', 'Target', 'Message', 'Type', 'Last seen'])}
					<tbody class="divide-y divide-line-soft">
						{#each events as e, i (i)}
							<tr
								onclick={() => e.namespace && e.name && onselect(e.namespace, e.name)}
								class="cursor-pointer hover:bg-select-soft {e.type === 'Warning'
									? 'bg-warn-soft/40'
									: ''}"
							>
								<td class="px-3 py-1.5 font-medium text-ink-soft">{e.reason}</td>
								<td class="px-3 py-1.5 text-ink">
									{e.name} <span class="text-ink-faint">· {e.namespace}</span>
								</td>
								<td class="px-3 py-1.5 text-ink-soft">{e.message}</td>
								<td class="px-3 py-1.5">
									<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
										<StatusDot tone={e.type === 'Warning' ? 'warn' : 'neutral'} size="xs" />
										{e.type}
									</span>
								</td>
								<td class="px-3 py-1.5 whitespace-nowrap text-ink-muted">
									{duration(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-ink-faint">
											×{e.count}</span
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
