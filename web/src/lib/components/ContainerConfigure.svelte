<script lang="ts">
	import type { Project } from '#lib/api.js';
	import { projectGate, projectNS } from '#lib/gates.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import Consequence from './Consequence.svelte';
	import DRSCard from './DRSCard.svelte';
	import InfoCard from './InfoCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import Row from './Row.svelte';
	import StageChip from './StageChip.svelte';

	// The container Configure tab: read-only project settings for compute
	// containers, cluster services (DRS) at the Hosts root. dotvirt owns nothing
	// here - projects are namespace labels, config is the repo. Node and segment
	// facts live on their own object pages.
	let {
		projects,
		cluster = false,
	}: {
		projects: Project[];
		cluster?: boolean;
	} = $props();
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		{#if cluster}
			<DRSCard />
		{/if}
		{#each projects as p (p.name)}
			<InfoCard title="Project: {p.name}">
				<dl class="divide-y divide-line-soft text-[13px]">
					<Row label="Repository">
						{#if p.repo}
							<a
								href={p.repo}
								target="_blank"
								class="font-mono text-xs text-accent-ink hover:underline">{p.repo}</a
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
					<p class="border-t border-warn-soft bg-warn-soft/60 px-card py-2 text-xs text-warn-ink">
						{p.error}
					</p>
				{/if}
				{#if p.repo}
					{@const sync = projectGate.syncAccess(projectNS(p))}
					<div class="flex flex-col gap-1.5 border-t border-line-soft px-card py-3">
						<div class="flex flex-wrap items-center gap-2">
							<Button
								variant="outline"
								size="sm"
								disabled={!sync.ok}
								title={sync.why}
								onclick={() => (ui.modal = { kind: 'syncAccess', project: p.name })}
								>Grant sync access</Button
							>
							<StageChip kind="pr" />
						</div>
						<Consequence size="sm"
							>ArgoCD syncs {p.name} as its own account once the platform PR merges.</Consequence
						>
					</div>
				{/if}
				<!-- Quota-aware capacity: the project's ResourceQuotas. -->
				<div class="border-t border-line-soft px-card py-2">
					<QuotaBand scope={{ project: p.name }} showEmpty />
				</div>
			</InfoCard>
		{/each}
	</div>
</div>
