<script lang="ts">
	import type { Project } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { projectGate, projectNS, publishGate } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { modules } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import Consequence from './Consequence.svelte';
	import RebalanceCard from './RebalanceCard.svelte';
	import InfoCard from './InfoCard.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import Row from './Row.svelte';
	import StageChip from './StageChip.svelte';

	// The container Configure tab: read-only project settings for compute
	// containers, cluster services (rebalancing) at the Hosts root. dotvirt owns nothing
	// here - projects are namespace labels, config is the repo. Node and network
	// facts live on their own object pages.
	let {
		projects,
		cluster = false,
	}: {
		projects: Project[];
		cluster?: boolean;
	} = $props();

	const domain = $derived(modules.value?.publishDomain ?? '');
</script>

<div class="grid items-start gap-4 lg:grid-cols-2">
	{#if cluster}
		<RebalanceCard />
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
						<span class="text-ink-muted">Not configured</span>
					{/if}
				</Row>
				<Row label="Namespaces">
					{#each p.namespaces as n (n.namespace)}
						<span
							class="ml-1 inline-block rounded bg-inset-strong px-1.5 py-0.5 text-xs text-ink-soft"
							><span class="font-mono">{n.namespace}</span>, {plural(n.vms.length, 'VM')}</span
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
			{#if domain && p.namespaces.length}
				<div class="flex flex-col gap-1.5 border-t border-line-soft px-card py-3">
					<span class="text-xs font-semibold text-ink-soft">Publishing</span>
					{#each p.namespaces as n (n.namespace)}
						{@const c = caps.ns(n.namespace)}
						{@const gate = publishGate.on(n.namespace)}
						<div class="flex flex-wrap items-center gap-2 text-[13px]">
							<span class="font-mono text-xs">{n.namespace}</span>
							{#if c?.publishingOn}
								<span class="text-ink-soft"
									>On, at <span class="font-mono">*.{n.namespace}.{domain}</span></span
								>
							{:else if c?.publishingWaits}
								<span class="text-ink-muted"
									>Not yet: a namespace with its own VM Network waits on a portail update.</span
								>
							{:else}
								<Button
									variant="outline"
									size="sm"
									disabled={!gate.ok}
									title={gate.why}
									onclick={() =>
										(ui.modal = { kind: 'publishing', project: p.name, namespace: n.namespace })}
									>Turn publishing on</Button
								>
								<StageChip kind="pr" />
							{/if}
						</div>
					{/each}
				</div>
			{/if}
			<!-- Quota-aware capacity: the project's ResourceQuotas. -->
			<div class="border-t border-line-soft px-card py-2">
				<QuotaBand scope={{ project: p.name }} showEmpty />
			</div>
		</InfoCard>
	{/each}
</div>
