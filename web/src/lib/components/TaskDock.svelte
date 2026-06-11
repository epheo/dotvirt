<script lang="ts">
	import { ChevronDown, ChevronUp, ListChecks } from 'lucide-svelte';
	import { api, type DraftView, type Inventory, type Proposal, type VMEvent } from '$lib/api';

	let {
		drafts,
		proposals,
		inventory,
		username,
		onselect
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
		inventory: Inventory | null;
		username: string;
		onselect: (namespace: string, name: string) => void;
	} = $props();

	let openPane = $state(true);
	let tab = $state<'tasks' | 'events'>('tasks');

	// Events lane: fetched on demand when the Events tab is opened (not on the
	// broadcast hot path), so a busy cluster's event churn can't spam the UI.
	let events = $state<VMEvent[] | null>(null);
	let eventsLoading = $state(false);

	function loadEvents() {
		eventsLoading = true;
		api
			.allEvents()
			.then((e) => (events = e))
			.catch(() => (events = []))
			.finally(() => (eventsLoading = false));
	}

	function selectTab(t: 'tasks' | 'events') {
		tab = t;
		openPane = true;
		if (t === 'events') loadEvents(); // refresh on each open
	}

	type Task = {
		kind: 'staged' | 'pr' | 'drift';
		verb: string;
		namespace: string;
		name: string;
		prTitle: string;
		status: string;
		by: string;
		project: string;
		url: string;
	};

	// One unified feed ordered by lifecycle stage, not timestamp: staged changes
	// (the draft) → open PRs (proposed) → standing drift (cluster ≠ git).
	const tasks = $derived.by<Task[]>(() => {
		const out: Task[] = [];
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

	const alarms = $derived(tasks.filter((t) => t.kind === 'drift').length);

	const dotClass = (k: Task['kind']) =>
		k === 'drift' ? 'bg-amber-500' : k === 'pr' ? 'bg-emerald-500' : 'bg-blue-500';
	const textClass = (k: Task['kind']) =>
		k === 'drift' ? 'text-amber-700' : k === 'pr' ? 'text-emerald-700' : 'text-slate-600';
	const rowClass = (k: Task['kind']) =>
		k === 'drift' ? 'bg-amber-50/40' : k === 'pr' ? 'bg-emerald-50/30' : '';

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
		{#if alarms > 0}
			<span class="rounded-full bg-amber-200 px-1.5 text-[11px] font-medium text-amber-800">
				{alarms} alarm{alarms > 1 ? 's' : ''}
			</span>
		{/if}
		<button
			onclick={() => (openPane = !openPane)}
			class="ml-auto p-1 text-slate-400 hover:text-slate-600"
			title="Collapse/expand"
		>
			{#if openPane}<ChevronDown size={14} />{:else}<ChevronUp size={14} />{/if}
		</button>
	</div>

	{#if openPane}
		<div class="max-h-48 overflow-y-auto">
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
							{#each tasks as t (t.kind + ':' + t.project + ':' + t.namespace + '/' + t.name + ':' + t.url)}
								<tr onclick={() => activate(t)} class="cursor-pointer hover:bg-blue-50 {rowClass(t.kind)}">
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
											<span class="h-1.5 w-1.5 rounded-full {dotClass(t.kind)}"></span>
											<span class={textClass(t.kind)}>{t.status}</span>
										</span>
									</td>
									<td class="px-3 py-1.5 text-slate-600">{t.by}</td>
									<td class="px-3 py-1.5 text-slate-500">{t.project}</td>
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
