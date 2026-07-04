<script lang="ts">
	import { api, drsThresholdLabel, type DRSView } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import InfoCard from './InfoCard.svelte';
	import Row from './Row.svelte';
	import DRSModal from './DRSModal.svelte';

	// The cluster's DRS panel (vCenter: Cluster → Configure → vSphere DRS):
	// committed configuration from the platform repo, live operator state from
	// the backend's KubeDescheduler snapshot. Polls like the metrics cards — the
	// GET is a pure snapshot read.
	let { onstaged }: { onstaged?: () => void } = $props();

	let view = $state<DRSView | null>(null);
	let error = $state('');
	let configuring = $state(false);
	let disabling = $state(false);

	async function load() {
		try {
			view = await api.drs();
			error = '';
		} catch (e) {
			error = String(e);
		}
	}
	$effect(() => {
		load();
		return pollWhileVisible(load, 30_000);
	});

	// One vCenter-style status line for the committed state.
	const status = $derived.by(() => {
		if (!view?.configured) return 'Not configured';
		switch (view.config?.mode) {
			case 'Predictive':
				return 'Predictive — recommendations only, no VM moves';
			case 'Automatic':
				return 'Fully automated — VMs live-migrate off hot nodes';
			default:
				return 'Configured (hand-edited manifest)';
		}
	});

	// The caller's staged, not-yet-proposed change.
	const pending = $derived.by(() => {
		const d = view?.draft;
		if (!d) return '';
		if (d.disableStaged) return 'Disable staged — propose it from "Changes"';
		return 'Change staged — propose it from "Changes"';
	});

	// The live plane, relative to what's committed: installing / running /
	// degraded — or explicitly unknown while the watch is stale or pre-sync.
	const liveStatus = $derived.by(() => {
		if (!view) return '';
		const l = view.live;
		if (l.stale) return 'Status unavailable — the descheduler watch is failing';
		if (l.degraded) return `Operator degraded: ${l.degraded}`;
		if (l.deployed) return l.available ? 'Operator running' : 'Operator starting';
		if (l.apiPresent && !l.synced) return 'Reading descheduler state…';
		if (view.configured) {
			return l.apiPresent ? 'Waiting for the configuration to sync' : 'Operator installing (OLM)';
		}
		return l.apiPresent ? 'Operator installed, no configuration' : 'Operator not installed';
	});

	async function disable() {
		disabling = true;
		error = '';
		try {
			await api.disableDRS();
			onstaged?.();
			await load();
		} catch (e) {
			error = String(e);
		} finally {
			disabling = false;
		}
	}

	function staged() {
		onstaged?.();
		load();
	}
</script>

<InfoCard title="DRS — automatic VM rebalancing">
	{#snippet action()}
		{#if view?.canManage}
			<span class="flex items-center gap-3">
				{#if view.configured}
					<button
						onclick={disable}
						disabled={disabling}
						class="text-xs text-red-600 hover:underline disabled:text-ink-faint">Disable…</button
					>
				{/if}
				<button onclick={() => (configuring = true)} class="text-xs text-accent hover:underline"
					>{view.configured ? 'Configure' : 'Enable DRS'}</button
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
				<Row label="Aggressiveness" value={drsThresholdLabel(view.config.threshold)} />
				<Row label="Interval" value={`${view.config.intervalSeconds}s`} />
				<Row label="Soft-taint hot nodes" value={view.config.softTainter ? 'Yes' : 'No'} />
				<Row
					label="Migration limits"
					value={`${view.config.evictionNodeLimit} per node · ${view.config.evictionTotalLimit} cluster-wide`}
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
			<p class="border-t border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700">
				{view.warning}
			</p>
		{/if}
		{#if view.live.degraded}
			<p class="border-t border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700">
				{view.live.degraded}
			</p>
		{/if}
		{#if !view.configured}
			<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-faint">
				Without DRS, VMs are placed once at start and stay put. Enabling stages the descheduler
				operator + configuration into the platform repository — applied when the PR merges.
			</p>
		{/if}
		{#if error}
			<p class="border-t border-red-100 bg-red-50 px-3 py-2 text-xs text-red-700">{error}</p>
		{/if}
	{/if}
</InfoCard>

{#if configuring && view}
	<DRSModal {view} onclose={() => (configuring = false)} onstaged={staged} />
{/if}
