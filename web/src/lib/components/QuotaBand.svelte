<script lang="ts">
	import { untrack } from 'svelte';
	import { api, Unauthorized, type NamespaceQuota } from '$lib/api';
	import { bytes, cores } from '$lib/format';

	// ResourceQuota usage bars for a container scope (project / namespace) —
	// the quota-aware capacity band. Self-contained: fetches its own data, so
	// the cluster Summary and the container Configure both just mount it.
	let {
		scope,
		showEmpty = false
	}: {
		scope: { project?: string; namespace?: string };
		showEmpty?: boolean; // render a note when no quotas exist (Configure)
	} = $props();

	let quotas = $state<NamespaceQuota[] | null>(null);

	// Parents pass an inline scope literal (recreated each render); key on its
	// value so refetch fires on real scope changes only, not every parent render.
	const key = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}`);
	$effect(() => {
		key;
		untrack(load);
	});
	async function load() {
		const s = scope;
		try {
			quotas = await api.quotas({ project: s.project, namespace: s.namespace });
		} catch (e) {
			if (e instanceof Unauthorized) return;
			quotas = [];
		}
	}

	function fmt(unit: string, v: number): string {
		if (unit === 'bytes') return bytes(v);
		if (unit === 'cores') return cores(v);
		return String(v);
	}
	const pct = (used: number, hard: number) => (hard > 0 ? Math.min(100, (used / hard) * 100) : 0);
	// vCenter-style escalation as usage nears the cap.
	const barColor = (p: number) => (p > 90 ? '#dc2626' : p > 75 ? '#f59e0b' : '#2563eb');
</script>

{#if quotas && quotas.length}
	<div class="flex flex-wrap gap-x-10 gap-y-3">
		{#each quotas as q (q.namespace + '/' + q.name)}
			<div class="min-w-[16rem]">
				<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">
					Quota — {q.namespace} <span class="font-normal normal-case text-slate-400">({q.name})</span>
				</div>
				<div class="mt-1.5 space-y-1.5">
					{#each q.items as it (it.resource)}
						{@const p = pct(it.used, it.hard)}
						<div>
							<div class="flex items-baseline justify-between gap-3 text-xs">
								<span class="font-mono text-[11px] text-slate-500">{it.resource}</span>
								<span class="text-slate-700"
									>{fmt(it.unit, it.used)} <span class="text-slate-400">of {fmt(it.unit, it.hard)}</span></span
								>
							</div>
							<div class="mt-0.5 h-1.5 overflow-hidden rounded-full bg-slate-100">
								<div class="h-full rounded-full" style="width:{p}%;background-color:{barColor(p)}"></div>
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/each}
	</div>
{:else if showEmpty && quotas}
	<p class="text-xs text-slate-400">No ResourceQuotas in scope — capacity is unbounded.</p>
{/if}
