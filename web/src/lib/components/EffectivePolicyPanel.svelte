<script lang="ts">
	import { api, Unauthorized, type EffectivePolicy, type PolicyBinding } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import PolicyRuleTable from './PolicyRuleTable.svelte';
	import SyncBadge from './SyncBadge.svelte';

	// The "what governs this workload" panel: GET .../policy returns the policy
	// chain already in evaluation order — admin tiers by precedence, then the
	// project rules that select the workload, then baseline — plus the egress
	// planes. VM scope resolves pod selectors against the workload's labels; a
	// namespace scope can't, so those bindings arrive marked conditional.
	let { namespace, vm }: { namespace: string; vm?: string } = $props();

	let eff = $state<EffectivePolicy | null>(null);
	let error = $state('');

	$effect(() => {
		const ns = namespace;
		const v = vm;
		eff = null;
		error = '';
		(v ? api.vmPolicy(ns, v) : api.namespacePolicy(ns))
			.then((e) => {
				// Drop a stale response if the scope moved while it was in flight.
				if (ns === namespace && v === vm) eff = e;
			})
			.catch((e) => {
				if (e instanceof Unauthorized) return;
				if (ns === namespace && v === vm) error = friendlyError(e);
			});
	});

	const keyOf = (b: PolicyBinding) =>
		`${b.policy.backing}:${b.policy.namespace ?? ''}:${b.policy.name}`;

	const tierLabel = (b: PolicyBinding) =>
		b.policy.kind === 'admin'
			? `Admin · priority ${b.policy.priority ?? 0}`
			: b.policy.kind === 'baseline'
				? 'Baseline'
				: 'Project';

	const denyText = $derived.by(() => {
		if (!eff) return '';
		const dirs = [
			eff.defaultDenyIngress ? 'ingress' : '',
			eff.defaultDenyEgress ? 'egress' : '',
		].filter(Boolean);
		if (!dirs.length) return '';
		return `Project rules select this workload: ${dirs.join(' and ')} not explicitly allowed below is denied.`;
	});
</script>

{#snippet bindingRow(b: PolicyBinding, tier: string | null)}
	<div class="border-b border-line-soft px-3 py-2 last:border-b-0">
		<div class="flex items-center gap-2">
			{#if tier}
				<span class="rounded bg-accent-soft px-1.5 py-0.5 text-xs whitespace-nowrap text-accent-ink"
					>{tier}</span
				>
			{/if}
			<span class="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{b.policy.name}</span>
			<span
				class="hidden max-w-56 truncate text-xs text-ink-muted sm:inline"
				title={b.policy.target}>{b.policy.target}</span
			>
			{#if b.conditional}
				<span
					class="rounded bg-warn-soft px-1.5 py-0.5 text-xs whitespace-nowrap text-warn-ink"
					title="Targets specific pods; this view can't resolve which">matching pods only</span
				>
			{/if}
			{#if b.policy.sync}
				<SyncBadge sync={b.policy.sync} error={b.policy.syncError ?? ''} compact />
			{/if}
		</div>
		{#if b.note}
			<p class="mt-1 text-xs text-ink-faint">{b.note}</p>
		{/if}
		{#if b.policy.rules?.length}
			<div class="mt-1.5">
				<PolicyRuleTable rules={b.policy.rules} />
			</div>
		{:else}
			<p class="mt-1 text-xs text-ink-faint">
				No rules{b.policy.kind === 'dfw'
					? ' — the selected pods default-deny all other ingress'
					: ''}.
			</p>
		{/if}
	</div>
{/snippet}

{#snippet plane(title: string, hint: string, list: PolicyBinding[] | undefined, empty: string)}
	<section class="rounded border border-line bg-panel">
		<header class="flex items-center gap-2 border-b border-line px-3 py-2">
			<h2 class="text-sm font-semibold text-ink">{title}</h2>
			<span class="min-w-0 flex-1 truncate text-xs text-ink-faint">{hint}</span>
		</header>
		{#if !list?.length}
			<p class="px-3 py-3 text-xs text-ink-faint">{empty}</p>
		{:else}
			{#each list as b (keyOf(b))}
				{@render bindingRow(b, null)}
			{/each}
		{/if}
	</section>
{/snippet}

{#if error}
	<p class="text-xs text-danger-ink">Effective policy unavailable: {error}</p>
{:else if !eff}
	<p class="text-xs text-ink-faint">Loading…</p>
{:else}
	<div class="max-w-3xl space-y-4">
		<div class="flex flex-wrap items-center gap-1.5 text-xs text-ink-muted">
			{#if eff.vm}
				<span>
					Matched against {eff.labelsLive ? 'the running instance labels' : 'the manifest labels'}:
				</span>
				{#if eff.labels && Object.keys(eff.labels).length}
					{#each Object.entries(eff.labels) as [k, v] (k)}
						<span class="rounded bg-inset px-1.5 py-0.5 text-ink-muted">{k}={v}</span>
					{/each}
				{:else}
					<span class="text-ink-faint">none — only selector-free policies can single it out</span>
				{/if}
			{:else}
				<span>
					Namespace-wide view: rules targeting specific pods are marked "matching pods only".
				</span>
			{/if}
		</div>

		<section class="rounded border border-line bg-panel">
			<header class="flex items-center gap-2 border-b border-line px-3 py-2">
				<h2 class="text-sm font-semibold text-ink">Distributed Firewall — east-west</h2>
				<span class="min-w-0 flex-1 truncate text-xs text-ink-faint"
					>Evaluated top to bottom; admin tiers override, baseline is the last resort</span
				>
			</header>
			{#if denyText}
				<p class="border-b border-line-soft bg-warn-soft/40 px-3 py-2 text-xs text-ink-soft">
					{denyText}
				</p>
			{/if}
			{#if !eff.eastWest?.length}
				<p class="px-3 py-3 text-xs text-ink-faint">
					No firewall rules bind this workload — east-west traffic is unrestricted.
				</p>
			{:else}
				{#each eff.eastWest as b (keyOf(b))}
					{@render bindingRow(b, tierLabel(b))}
				{/each}
			{/if}
		</section>

		{@render plane(
			'Gateway Firewall',
			'North-south egress — first match wins',
			eff.gateway,
			'No egress firewall — external egress is unrestricted.',
		)}
		{@render plane(
			'SNAT (Tier-0)',
			'The egress IP pool this workload leaves through',
			eff.snat,
			'No SNAT pool — egress uses the node IP.',
		)}
		{@render plane(
			'External routes (Tier-0)',
			'Policy-based next hops steering this egress',
			eff.routes,
			'No policy-based routes — egress follows the default gateway.',
		)}
	</div>
{/if}
