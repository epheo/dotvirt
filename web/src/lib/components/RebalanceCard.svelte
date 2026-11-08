<script lang="ts">
	import { api, rebalanceThresholdLabel, type DRSView } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import Card from './Card.svelte';
	import Consequence from './Consequence.svelte';
	import Row from './Row.svelte';
	import StageChip from './StageChip.svelte';
	import RebalanceModal from './RebalanceModal.svelte';

	// The cluster's rescheduling panel:
	// committed configuration from the platform repo, live operator state from
	// the backend's KubeDescheduler snapshot. Polls like the metrics cards - the
	// GET is a pure snapshot read.
	let configuring = $state(false);
	const disableOp = action(); // a failed disable, distinct from the read's failure

	const rebalance = resource<DRSView>(
		() => '',
		() => api.rebalancing(),
		{ poll: 30_000 },
	);
	const view = $derived(rebalance.data);
	const error = $derived(disableOp.error || rebalance.error);
	const enable = $derived(clusterGate.rebalanceOn());
	const off = $derived(clusterGate.rebalanceOff());

	// One status line for the committed state.
	const status = $derived.by(() => {
		if (!view?.configured) return 'Not configured';
		switch (view.config?.mode) {
			case 'Predictive':
				return 'Predictive: recommendations only, no VM moves';
			case 'Automatic':
				return 'Fully automated: VMs live-migrate off hot hosts';
			default:
				return 'Configured (hand-edited manifest)';
		}
	});

	// The caller's staged, not-yet-proposed change.
	const pending = $derived.by(() => {
		const d = view?.draft;
		if (!d) return '';
		if (d.disableStaged) return 'Disable staged; propose it from Changes';
		return 'Change staged; propose it from Changes';
	});

	// The live plane, relative to what's committed: installing / running /
	// degraded - or explicitly unknown while the watch is stale or pre-sync.
	const liveStatus = $derived.by(() => {
		if (!view) return '';
		const l = view.live;
		if (l.stale) return 'Status unavailable: dotvirt cannot read the descheduler state right now';
		if (l.degraded) return `Operator degraded: ${l.degraded}`;
		if (l.deployed) return l.available ? 'Operator running' : 'Operator starting';
		if (l.apiPresent && !l.synced) return 'Reading descheduler state...';
		if (view.configured) {
			return l.apiPresent ? 'Waiting for the configuration to sync' : 'Operator installing (OLM)';
		}
		return l.apiPresent ? 'Operator installed, no configuration' : 'Operator not installed';
	});

	function disable() {
		return disableOp.run(async () => {
			await api.disableRebalancing();
			ui.toastStaged();
			await rebalance.refresh();
		});
	}
</script>

<Card title="Rebalancing" meta="Dynamic Rescheduling" flush>
	{#if !view}
		<p class="px-card py-3 text-[13px] text-ink-muted">{error || 'Loading...'}</p>
	{:else}
		<dl class="divide-y divide-line-soft text-[13px]">
			<Row label="Status" value={status} />
			{#if view.config}
				<Row label="Aggressiveness" value={rebalanceThresholdLabel(view.config.threshold)} />
				<Row label="Interval" value={`${view.config.intervalSeconds}s`} />
				<Row label="Soft-taint hot hosts" value={view.config.softTainter ? 'Yes' : 'No'} />
				<Row
					label="Migration limits"
					value={`${view.config.evictionNodeLimit} per host, ${view.config.evictionTotalLimit} cluster-wide`}
				/>
			{/if}
			<Row
				label="PSI (load signal)"
				value={view.psiConfigured ? 'Managed by dotvirt' : 'Not managed'}
			/>
			{#if pending}
				<Row label="Pending" value={pending} />
			{/if}
			<Row label="Live state" value={liveStatus} />
		</dl>
		{#if view.warning}
			<p class="border-t border-warn-soft bg-warn-soft/60 px-card py-2 text-xs text-warn-ink">
				{view.warning}
			</p>
		{/if}
		{#if view.live.degraded}
			<p class="border-t border-warn-soft bg-warn-soft/60 px-card py-2 text-xs text-warn-ink">
				{view.live.degraded}
			</p>
		{/if}
		<div class="flex flex-col gap-1.5 border-t border-line-soft px-card py-3">
			{#if view.configured || enable.ok}
				<div class="flex flex-wrap items-center gap-2">
					<Button
						variant="outline"
						size="sm"
						disabled={!enable.ok}
						title={enable.why}
						onclick={() => (configuring = true)}
						>{view.configured ? 'Configure' : 'Enable Dynamic Rescheduling'}</Button
					>
					{#if view.configured}
						<Button
							variant="danger-secondary"
							size="sm"
							onclick={disable}
							disabled={disableOp.busy || !off.ok}
							title={off.why}>Disable...</Button
						>
					{/if}
					<StageChip kind="pr" />
				</div>
			{/if}
			{#if view.configured}
				<Consequence size="sm"
					>Changes stage into the platform repository and apply when the PR merges.</Consequence
				>
			{:else if enable.ok}
				<Consequence size="sm" tone="info"
					>VMs are placed once at start and stay put. Enabling stages the descheduler operator and
					its configuration into the platform repository; it applies when the PR merges.</Consequence
				>
			{:else}
				<p class="text-xs leading-4 text-ink-muted">
					VMs are placed once at start and stay put. {enable.why}
				</p>
			{/if}
		</div>
		{#if error}
			<p class="border-t border-danger-soft bg-danger-soft/60 px-card py-2 text-xs text-danger-ink">
				{error}
			</p>
		{/if}
	{/if}
</Card>

{#if configuring && view}
	<RebalanceModal
		{view}
		onclose={() => (configuring = false)}
		onstaged={() => rebalance.refresh()}
	/>
{/if}
