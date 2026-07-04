<script lang="ts">
	import { untrack } from 'svelte';
	import { api, Unauthorized, type ClusterSummary } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import HostBalance from './HostBalance.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import Ring from './Ring.svelte';
	import TopConsumers from './TopConsumers.svelte';

	let {
		scope = {},
		onselect,
	}: {
		scope?: { project?: string; namespace?: string; node?: string };
		onselect?: (namespace: string, name: string) => void;
	} = $props();

	let data = $state<ClusterSummary | null>(null);

	let loading = $state(false);
	let failed = $state(false);

	async function load() {
		if (!data) loading = true; // spinner only on first load, not on a poll refresh
		try {
			data = await api.clusterSummary(scope);
			failed = false;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			failed = true;
		} finally {
			loading = false;
		}
	}
	// Re-fetch when the container scope changes, keyed on a stable string so the
	// reload fires on real scope changes only (untrack the load's scope reads).
	const scopeKey = $derived(`${scope.project ?? ''}|${scope.namespace ?? ''}|${scope.node ?? ''}`);
	$effect(() => {
		scopeKey;
		untrack(load);
	});
	// Refresh on a cadence, paused while the tab is backgrounded.
	$effect(() => pollWhileVisible(load, 30000));

	// KubeVirt's phase label is lowercase ("running"); order known phases, capitalize
	// for display, and tolerate any others.
	const PHASE_ORDER = [
		'running',
		'paused',
		'stopped',
		'pending',
		'scheduling',
		'succeeded',
		'failed',
	];
	const phaseColor: Record<string, string> = {
		running: 'text-green-600',
		paused: 'text-amber-600',
		failed: 'text-red-600',
	};
	const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

	// Overcommit ratio = committed-to-VMs : node-allocatable (vCenter's "vCPU
	// 3.2:1"). >1 means more is promised to VMs than the nodes physically have —
	// fine for CPU (time-shared), a memory warning. Only meaningful with both a
	// committed amount and a capacity denominator.
	const overcommit = $derived.by(() => {
		if (!data) return [];
		const ratio = (m: { allocated?: number; total: number }) =>
			m.allocated && m.total > 0 ? m.allocated / m.total : 0;
		return [
			{ label: 'vCPU', r: ratio(data.cpu) },
			{ label: 'Memory', r: ratio(data.memory) },
		].filter((x) => x.r > 0);
	});
</script>

{#if data}
	<div class="border-b border-slate-200 bg-slate-50 px-4 py-3">
		<div class="flex flex-wrap items-start gap-x-8 gap-y-3">
			<div class="flex gap-6">
				<Ring
					label="CPU"
					used={data.cpu.used}
					total={data.cpu.total}
					allocated={data.cpu.allocated ?? 0}
					unit="cores"
					color="#2563eb"
					spark={data.cpu.spark ?? []}
				/>
				<Ring
					label="Memory"
					used={data.memory.used}
					total={data.memory.total}
					allocated={data.memory.allocated ?? 0}
					unit="bytes"
					color="#0d9488"
					spark={data.memory.spark ?? []}
				/>
				<Ring
					label="Storage (guest)"
					used={data.storage.used}
					total={data.storage.total}
					unit="bytes"
					color="#7c3aed"
					spark={data.storage.spark ?? []}
				/>
			</div>

			<div>
				<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">
					Virtual machines
				</div>
				<div class="mt-2 flex gap-4">
					{#each Object.entries(data.vms)
						.filter(([, n]) => n > 0)
						.sort(([a], [b]) => PHASE_ORDER.indexOf(a) - PHASE_ORDER.indexOf(b)) as [phase, n] (phase)}
						<div class="text-center">
							<div class="text-xl font-semibold {phaseColor[phase] ?? 'text-slate-700'}">{n}</div>
							<div class="text-[11px] text-slate-500">{cap(phase)}</div>
						</div>
					{/each}
				</div>
			</div>

			{#if overcommit.length}
				<div>
					<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">Overcommit</div>
					<div class="mt-2 flex gap-2">
						{#each overcommit as o (o.label)}
							<span
								class="rounded px-2 py-1 text-sm font-medium {o.r > 1
									? 'bg-amber-100 text-amber-800'
									: 'bg-slate-100 text-slate-600'}"
								title="{o.label} committed to VMs vs node-allocatable"
							>
								{o.label}
								{o.r.toFixed(1)}:1
							</span>
						{/each}
					</div>
				</div>
			{/if}
		</div>

		<!-- Quota-aware capacity: ResourceQuota bars at project/namespace scope —
		     the tenant's real boundary, where node-allocatable is the cluster's. -->
		{#if scope.project || scope.namespace}
			<div class="mt-3">
				<QuotaBand scope={{ project: scope.project, namespace: scope.namespace }} />
			</div>
		{/if}
	</div>

	<!-- The detail row: full cards below the glance band, using the page's height
	     instead of cramming everything onto one strip. -->
	<div class="grid items-start gap-4 p-4 lg:grid-cols-2">
		<!-- Cluster scope only: the worker distribution is one cluster-wide fact;
		     a project/namespace/node view would repeat it misleadingly. -->
		{#if !scope.project && !scope.namespace && !scope.node}
			<HostBalance />
		{/if}
		<TopConsumers topCpu={data.topCpu} topMemory={data.topMemory} {onselect} />
	</div>
{:else if loading}
	<div class="border-b border-slate-200 bg-slate-50 px-4 py-6 text-center text-sm text-slate-400">
		Loading cluster metrics…
	</div>
{:else if failed}
	<div class="border-b border-slate-200 bg-slate-50 px-4 py-6 text-center text-sm text-slate-400">
		Cluster metrics unavailable.
	</div>
{/if}
