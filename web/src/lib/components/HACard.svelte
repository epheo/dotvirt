<script lang="ts">
	import { api, type HAView } from '$lib/api';
	import { action, resource } from '$lib/resource.svelte';
	import InfoCard from './InfoCard.svelte';
	import Row from './Row.svelte';
	import HAModal from './HAModal.svelte';

	// The cluster's HA panel (vCenter: Cluster -> Configure -> vSphere HA):
	// committed configuration from the platform repo, live operator state from
	// the backend's NodeHealthCheck snapshot. Polls like the DRS card - the GET
	// is a pure snapshot read.
	let { onstaged }: { onstaged?: () => void } = $props();

	let configuring = $state(false);
	const disableOp = action(); // a failed disable, distinct from the read's failure

	const ha = resource<HAView>(
		() => '',
		() => api.ha(),
		{ poll: 30_000 },
	);
	const view = $derived(ha.data);
	const error = $derived(disableOp.error || ha.error);

	// One vCenter-style status line for the committed state.
	const status = $derived.by(() => {
		if (!view?.configured) return 'Not configured';
		if (!view.config) return 'Configured (hand-edited manifest)';
		return 'Protected — failed hosts are fenced and their VMs restart elsewhere';
	});

	// The caller's staged, not-yet-proposed change.
	const pending = $derived.by(() => {
		const d = view?.draft;
		if (!d) return '';
		if (d.disableStaged) return 'Disable staged — propose it from "Changes"';
		return 'Change staged — propose it from "Changes"';
	});

	// The live plane, relative to what's committed: installing / monitoring /
	// remediating - or explicitly unknown while the watch is stale or pre-sync.
	const liveStatus = $derived.by(() => {
		if (!view) return '';
		const l = view.live;
		if (l.stale) return 'Status unavailable — the node health watch is failing';
		if (l.remediating?.length) return `Fencing ${l.remediating.join(', ')}`;
		if (l.phase === 'Disabled') return `Disabled by the operator: ${l.reason || 'unknown reason'}`;
		if (l.phase === 'Paused') return 'Paused';
		if (l.phase) return `Monitoring — ${l.healthyNodes}/${l.observedNodes} hosts healthy`;
		if (l.deployed) return 'Operator starting';
		if (l.apiPresent && !l.synced) return 'Reading node health state…';
		if (view.configured) {
			return l.apiPresent ? 'Waiting for the configuration to sync' : 'Operator installing (OLM)';
		}
		return l.apiPresent ? 'Operator installed, no configuration' : 'Operator not installed';
	});

	function disable() {
		return disableOp.run(async () => {
			await api.disableHA();
			onstaged?.();
			await ha.refresh();
		});
	}

	function staged() {
		onstaged?.();
		ha.refresh();
	}
</script>

<InfoCard title="High Availability - restart VMs on host failure">
	{#snippet action()}
		{#if view?.canManage}
			<span class="flex items-center gap-3">
				{#if view.configured}
					<button
						onclick={disable}
						disabled={disableOp.busy}
						class="text-xs text-danger hover:underline disabled:text-ink-faint">Disable…</button
					>
				{/if}
				<button onclick={() => (configuring = true)} class="text-xs text-accent hover:underline"
					>{view.configured ? 'Configure' : 'Enable High Availability'}</button
				>
			</span>
		{/if}
	{/snippet}

	{#if !view}
		<p class="px-3 py-3 text-xs text-ink-faint">{error || 'Loading…'}</p>
	{:else}
		<dl class="divide-y divide-line-soft text-[13px]">
			<Row label="Status" value={status} />
			{#if view.config}
				<Row
					label="Host failure detection"
					value={`${view.config.unhealthySeconds}s unresponsive`}
				/>
				<Row label="Minimum healthy hosts" value={`${view.config.minHealthyPercent}%`} />
			{/if}
			{#if pending}
				<Row label="Pending" value={pending} />
			{/if}
			<Row label="Live state" value={liveStatus} />
		</dl>
		{#if view.warning}
			<p class="border-t border-warn-soft bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink">
				{view.warning}
			</p>
		{/if}
		{#if view.live.phase === 'Disabled' && view.live.reason}
			<p class="border-t border-warn-soft bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink">
				{view.live.reason}
			</p>
		{/if}
		{#if !view.configured}
			<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-faint">
				Without HA, VMs on a failed host stay down until the host returns. Enabling stages the Node
				Health Check operator + fencing configuration into the platform repository — applied when
				the PR merges. Unresponsive hosts are then fenced and their VMs restart on surviving hosts.
			</p>
		{/if}
		{#if error}
			<p class="border-t border-danger-soft bg-danger-soft/60 px-3 py-2 text-xs text-danger-ink">
				{error}
			</p>
		{/if}
	{/if}
</InfoCard>

{#if configuring && view}
	<HAModal {view} onclose={() => (configuring = false)} onstaged={staged} />
{/if}
