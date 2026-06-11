<script lang="ts">
	import { ChevronDown, ChevronUp, ListChecks } from 'lucide-svelte';
	import type { DraftView, Inventory } from '$lib/api';

	let {
		drafts,
		inventory,
		username,
		onselect
	}: {
		drafts: { project: string; draft: DraftView }[];
		inventory: Inventory | null;
		username: string;
		onselect: (namespace: string, name: string) => void;
	} = $props();

	let open = $state(true);

	type Task = {
		kind: 'staged' | 'drift';
		verb: string;
		namespace: string;
		name: string;
		status: string;
		by: string;
		project: string;
	};

	// One unified feed ordered by lifecycle, not timestamp: staged changes (the
	// draft, "future" work) and standing drift conditions (cluster ≠ git). Open PRs
	// and the KubeVirt event stream join in later passes — see the roadmap.
	const tasks = $derived.by<Task[]>(() => {
		const out: Task[] = [];
		for (const { project, draft } of drafts) {
			for (const it of draft.items) {
				out.push({
					kind: 'staged',
					verb: it.kind === 'edit' ? 'Reconfigure' : it.kind === 'create' ? 'Create' : 'Delete',
					namespace: it.namespace,
					name: it.name,
					status: 'Staged',
					by: username,
					project
				});
			}
		}
		if (inventory) {
			for (const p of inventory.projects)
				for (const ns of p.namespaces)
					for (const vm of ns.vms)
						if (vm.sync === 'OutOfSync')
							out.push({
								kind: 'drift',
								verb: 'Configuration drift',
								namespace: vm.namespace,
								name: vm.name,
								status: 'Drifted',
								by: '—',
								project: p.name
							});
		}
		return out;
	});

	const alarms = $derived(tasks.filter((t) => t.kind === 'drift').length);
</script>

<section class="border-t border-slate-300 bg-white text-xs">
	<!-- Header bar: always visible; toggles the pane like vCenter's Recent Tasks. -->
	<button
		onclick={() => (open = !open)}
		class="flex w-full items-center gap-2 bg-slate-100 px-3 py-1.5 text-left text-slate-600 hover:bg-slate-200"
	>
		<ListChecks size={14} class="text-slate-500" />
		<span class="font-semibold tracking-wide uppercase">Recent Tasks</span>
		<span class="rounded-full bg-slate-300 px-1.5 text-[11px] text-slate-700">{tasks.length}</span>
		{#if alarms > 0}
			<span class="rounded-full bg-amber-200 px-1.5 text-[11px] font-medium text-amber-800">
				{alarms} alarm{alarms > 1 ? 's' : ''}
			</span>
		{/if}
		<span class="ml-auto text-slate-400">
			{#if open}<ChevronDown size={14} />{:else}<ChevronUp size={14} />{/if}
		</span>
	</button>

	{#if open}
		<div class="max-h-48 overflow-y-auto">
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
						{#each tasks as t (t.kind + ':' + t.namespace + '/' + t.name)}
							<tr
								onclick={() => onselect(t.namespace, t.name)}
								class="cursor-pointer hover:bg-blue-50 {t.kind === 'drift' ? 'bg-amber-50/40' : ''}"
							>
								<td class="px-3 py-1.5 text-slate-700">{t.verb}</td>
								<td class="px-3 py-1.5 font-medium text-slate-800">
									{t.name} <span class="font-normal text-slate-400">· {t.namespace}</span>
								</td>
								<td class="px-3 py-1.5">
									<span class="inline-flex items-center gap-1.5">
										<span
											class="h-1.5 w-1.5 rounded-full {t.kind === 'drift' ? 'bg-amber-500' : 'bg-blue-500'}"
										></span>
										<span class={t.kind === 'drift' ? 'text-amber-700' : 'text-slate-600'}>{t.status}</span>
									</span>
								</td>
								<td class="px-3 py-1.5 text-slate-600">{t.by}</td>
								<td class="px-3 py-1.5 text-slate-500">{t.project}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/if}
</section>
