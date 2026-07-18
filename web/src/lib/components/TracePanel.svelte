<script lang="ts">
	import { Play } from 'lucide-svelte';
	import { api, Unauthorized, type TraceResult, type TraceStep } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { inventory } from '$lib/state/inventory.svelte';
	import { TONE_PILL, TONE_TEXT, type Tone } from '$lib/status';
	import PolicyRuleTable from './PolicyRuleTable.svelte';
	import SyncBadge from './SyncBadge.svelte';

	// Trace a flow: NSX Traceflow's question answered as a control-plane
	// simulation — walk the evaluation order for one concrete source,
	// destination and protocol/port, and show each step's verdict with the
	// deciding rule. No packet is injected; the panel says so.
	// source pins the panel to one VM (the VM Security tab); without it the
	// Security page offers free pickers.
	let { source }: { source?: { namespace: string; vm: string } } = $props();

	let pickNS = $state('');
	let pickVM = $state('');
	const srcNS = $derived(source?.namespace ?? pickNS);
	const srcVM = $derived(source?.vm ?? pickVM);
	let dstMode = $state<'vm' | 'ip'>('vm');
	let dstNS = $state('');
	let dstVM = $state('');
	let dstIP = $state('');
	let protocol = $state('TCP');
	let port = $state(''); // empty = any port

	let running = $state(false);
	let result = $state<TraceResult | null>(null);
	let error = $state('');

	const vmsIn = (ns: string) => inventory.allVMs.filter((v) => v.namespace === ns);
	const ready = $derived(
		!!srcNS && !!srcVM && (dstMode === 'vm' ? !!dstNS && !!dstVM : !!dstIP.trim()),
	);

	async function run() {
		if (!ready || running) return;
		running = true;
		error = '';
		result = null;
		try {
			result = await api.trace({
				source: { namespace: srcNS, vm: srcVM },
				destination: dstMode === 'vm' ? { namespace: dstNS, vm: dstVM } : { ip: dstIP.trim() },
				protocol,
				port: port === '' ? 0 : Number(port),
			});
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = friendlyError(e);
		} finally {
			running = false;
		}
	}

	const VERDICT_TONE: Record<string, Tone> = {
		Allow: 'ok',
		Deny: 'danger',
		Conditional: 'warn',
		Unreachable: 'neutral',
	};
	const VERDICT_TEXT: Record<string, string> = {
		Allow: 'Allowed — every evaluated control lets this flow through.',
		Deny: 'Denied — a rule below stops this flow.',
		Conditional: 'Unresolved — a rule below may match; see the flagged steps.',
		Unreachable: 'Unreachable — no shared network carries this flow.',
	};

	const STAGE_LABEL: Record<string, string> = {
		connectivity: 'Connectivity',
		segment: 'Segment',
		admin: 'Admin DFW',
		dfw: 'Project DFW',
		baseline: 'Baseline',
		default: 'Default',
		gateway: 'Gateway FW',
		snat: 'SNAT',
		route: 'Route',
	};
	const actionTone = (s: TraceStep): Tone =>
		s.action === 'Deny' || s.action === 'Unreachable'
			? 'danger'
			: s.action === 'Allow' || s.action === 'Reachable'
				? 'ok'
				: s.action === 'Bypass'
					? 'warn'
					: 'neutral';
</script>

<div class="space-y-3">
	<div class="flex flex-wrap items-end gap-2 text-xs">
		{#if !source}
			<label class="flex flex-col gap-1">
				<span class="text-ink-faint">Source VM</span>
				<span class="flex gap-1">
					<select
						bind:value={pickNS}
						onchange={() => (pickVM = '')}
						class="rounded border border-line-strong px-2 py-1"
					>
						<option value="" disabled>namespace</option>
						{#each inventory.namespaces as ns (ns)}
							<option value={ns}>{ns}</option>
						{/each}
					</select>
					<select bind:value={pickVM} class="rounded border border-line-strong px-2 py-1">
						<option value="" disabled>vm</option>
						{#each vmsIn(pickNS) as v (v.name)}
							<option value={v.name}>{v.name}</option>
						{/each}
					</select>
				</span>
			</label>
		{/if}
		<label class="flex flex-col gap-1">
			<span class="text-ink-faint">Destination</span>
			<span class="flex items-center gap-1">
				<select bind:value={dstMode} class="rounded border border-line-strong px-2 py-1">
					<option value="vm">VM</option>
					<option value="ip">External IP</option>
				</select>
				{#if dstMode === 'vm'}
					<select
						bind:value={dstNS}
						onchange={() => (dstVM = '')}
						class="rounded border border-line-strong px-2 py-1"
					>
						<option value="" disabled>namespace</option>
						{#each inventory.namespaces as ns (ns)}
							<option value={ns}>{ns}</option>
						{/each}
					</select>
					<select bind:value={dstVM} class="rounded border border-line-strong px-2 py-1">
						<option value="" disabled>vm</option>
						{#each vmsIn(dstNS) as v (v.name)}
							<option value={v.name}>{v.name}</option>
						{/each}
					</select>
				{:else}
					<input
						bind:value={dstIP}
						placeholder="203.0.113.9"
						class="w-36 rounded border border-line-strong px-2 py-1"
					/>
				{/if}
			</span>
		</label>
		<label class="flex flex-col gap-1">
			<span class="text-ink-faint">Protocol</span>
			<select bind:value={protocol} class="rounded border border-line-strong px-2 py-1">
				<option>TCP</option>
				<option>UDP</option>
				<option>SCTP</option>
			</select>
		</label>
		<label class="flex flex-col gap-1">
			<span class="text-ink-faint">Port</span>
			<input
				bind:value={port}
				type="number"
				min="1"
				max="65535"
				placeholder="any"
				class="w-20 rounded border border-line-strong px-2 py-1"
			/>
		</label>
		<button
			type="button"
			onclick={run}
			disabled={!ready || running}
			class="inline-flex items-center gap-1 rounded bg-accent px-2.5 py-1.5 font-medium text-white hover:bg-accent-hover disabled:bg-line-strong"
		>
			<Play size={12} />
			{running ? 'Tracing…' : 'Trace'}
		</button>
	</div>
	<p class="text-xs text-ink-faint">
		Simulates the live policy objects — no packet is injected, so datapath faults stay invisible to
		it.
	</p>

	{#if error}
		<p class="text-xs text-danger-ink">Trace failed: {error}</p>
	{:else if result}
		<div class="rounded border border-line bg-panel">
			<div class="flex items-center gap-2 border-b border-line px-3 py-2">
				<span
					class="rounded px-2 py-0.5 text-xs font-semibold {TONE_PILL[
						VERDICT_TONE[result.verdict] ?? 'neutral'
					]}">{result.verdict}</span
				>
				<span class="text-xs text-ink-muted">{VERDICT_TEXT[result.verdict] ?? ''}</span>
			</div>
			{#if !result.steps.length}
				<p class="px-3 py-3 text-xs text-ink-faint">
					No policy state available — the networking snapshot is empty.
				</p>
			{:else}
				<ol class="divide-y divide-line-soft">
					{#each result.steps as s, i (i)}
						<li class="px-3 py-2 {s.decisive ? 'border-l-2 border-l-accent' : ''}">
							<div class="flex items-center gap-2 text-xs">
								<span
									class="rounded bg-accent-soft px-1.5 py-0.5 whitespace-nowrap text-accent-ink"
								>
									{STAGE_LABEL[s.stage] ?? s.stage}{s.direction
										? ` · ${s.direction.toLowerCase()}`
										: ''}
								</span>
								<span class="font-medium {TONE_TEXT[actionTone(s)]}">{s.action}</span>
								{#if s.policy}
									<span class="min-w-0 truncate font-medium text-ink">{s.policy.name}</span>
									{#if s.policy.namespace}
										<span class="rounded bg-inset px-1.5 py-0.5 text-ink-muted"
											>{s.policy.namespace}</span
										>
									{/if}
									{#if s.policy.sync}
										<SyncBadge sync={s.policy.sync} error={s.policy.syncError ?? ''} compact />
									{/if}
								{/if}
								{#if s.conditional}
									<span
										class="rounded bg-warn-soft px-1.5 py-0.5 whitespace-nowrap text-warn-ink"
										title="This rule may match; the trace can't resolve it">may apply</span
									>
								{/if}
							</div>
							{#if s.rule}
								<div class="mt-1.5">
									<PolicyRuleTable rules={[s.rule]} />
								</div>
							{/if}
							{#if s.note}
								<p class="mt-1 text-xs text-ink-faint">{s.note}</p>
							{/if}
						</li>
					{/each}
				</ol>
			{/if}
		</div>
	{/if}
</div>
