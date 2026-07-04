<script lang="ts">
	import type { Project } from '$lib/api';
	import DRSCard from './DRSCard.svelte';
	import InfoCard from './InfoCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import Row from './Row.svelte';

	// The compute container's Configure tab: read-only project settings, plus
	// cluster services (DRS) at the cluster scope. dotvirt owns nothing here —
	// projects are namespace labels, config is the repo. Node and segment facts
	// live on their own object pages.
	let {
		projects,
		cluster = false,
		onstaged,
	}: {
		projects: Project[];
		cluster?: boolean;
		onstaged?: () => void; // a DRS change was staged — refresh the drafts badge
	} = $props();
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		{#if cluster}
			<!-- Cluster services (vCenter: Cluster → Configure → Services). -->
			<DRSCard {onstaged} />
		{/if}
		{#each projects as p (p.name)}
			<InfoCard title="Project: {p.name}">
				<dl class="divide-y divide-line-soft text-[13px]">
					<Row label="Repository">
						{#if p.repo}
							<a href={p.repo} target="_blank" class="font-mono text-xs text-accent hover:underline"
								>{p.repo}</a
							>
						{:else}
							<span class="text-ink-faint">— not configured</span>
						{/if}
					</Row>
					<Row label="Namespaces">
						{#each p.namespaces as n (n.namespace)}
							<span
								class="ml-1 inline-block rounded bg-inset-strong px-1.5 py-0.5 text-xs text-ink-soft"
								>{n.namespace} · {n.vms.length} VMs</span
							>
						{/each}
					</Row>
				</dl>
				{#if p.error}
					<p class="border-t border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700">
						{p.error}
					</p>
				{/if}
				<!-- Quota-aware capacity: the project's ResourceQuotas. -->
				<div class="border-t border-line-soft px-3 py-2">
					<QuotaBand scope={{ project: p.name }} showEmpty />
				</div>
			</InfoCard>
		{/each}
	</div>
</div>
