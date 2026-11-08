<script lang="ts">
	import { api, type NamespaceQuota } from '#lib/api.js';
	import { bytes, cores } from '#lib/format.js';
	import { resource } from '#lib/resource.svelte.js';
	import { usageLevelColor } from '#lib/status.js';
	import Card from './Card.svelte';

	// ResourceQuota usage bars for a container scope (project / namespace) -
	// the quota-aware capacity band. Self-contained: fetches its own data, so
	// the cluster Summary and the container Configure both just mount it. card
	// frames it as its own Summary card, absent when the scope has no quota.
	let {
		scope,
		showEmpty = false,
		card = false,
	}: {
		scope: { project?: string; namespace?: string };
		showEmpty?: boolean; // render a note when no quotas exist (Configure)
		card?: boolean;
	} = $props();

	// Parents pass an inline scope literal (recreated each render); key on its
	// value so refetch fires on real scope changes only, not every parent render.
	const key = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}`);
	const qRes = resource<NamespaceQuota[]>(
		() => key,
		() => api.quotas({ project: scope.project, namespace: scope.namespace }),
	);
	const quotas = $derived(qRes.failed ? [] : qRes.data);

	function fmt(unit: string, v: number): string {
		if (unit === 'bytes') return bytes(v);
		if (unit === 'cores') return cores(v);
		return String(v);
	}
	const pct = (used: number, hard: number) => (hard > 0 ? Math.min(100, (used / hard) * 100) : 0);
</script>

{#snippet bars(list: NamespaceQuota[])}
	<div class="flex flex-wrap gap-x-10 gap-y-3">
		{#each list as q (q.namespace + '/' + q.name)}
			<div class="min-w-[16rem]">
				<div class="text-xs font-semibold text-ink-muted">
					<span class="font-mono">{q.namespace}</span>
					<span class="font-normal text-ink-faint">quota {q.name}</span>
				</div>
				<div class="mt-1.5 space-y-1.5">
					{#each q.items as it (it.resource)}
						{@const p = pct(it.used, it.hard)}
						<div>
							<div class="flex items-baseline justify-between gap-3 text-xs">
								<span class="font-mono text-[11px] text-ink-muted">{it.resource}</span>
								<span class="text-ink-soft"
									>{fmt(it.unit, it.used)}
									<span class="text-ink-muted">of {fmt(it.unit, it.hard)}</span></span
								>
							</div>
							<div class="mt-0.5 h-1.5 overflow-hidden rounded-full bg-inset-strong">
								<div
									class="h-full rounded-full"
									style="width:{p}%;background-color:{usageLevelColor(p)}"
								></div>
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/each}
	</div>
{/snippet}

{#if quotas && quotas.length}
	{#if card}
		<Card title="Quota" meta={quotas.length > 1 ? `${quotas.length} quotas` : undefined}>
			{@render bars(quotas)}
		</Card>
	{:else}
		{@render bars(quotas)}
	{/if}
{:else if showEmpty && quotas}
	<p class="text-xs text-ink-muted">No ResourceQuotas in scope: capacity is unbounded.</p>
{/if}
