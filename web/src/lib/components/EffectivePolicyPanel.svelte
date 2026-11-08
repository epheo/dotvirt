<script lang="ts">
	import { api, type EffectivePolicy, type PolicyBinding } from '#lib/api.js';
	import { resource } from '#lib/resource.svelte.js';
	import { friendlyError } from '#lib/format.js';
	import { policyKindLabel } from '#lib/labels.js';
	import PolicyRuleTable from './PolicyRuleTable.svelte';
	import SyncBadge from './SyncBadge.svelte';

	// The "what governs this workload" panel: GET .../policy returns the policy
	// chain already in evaluation order - admin tiers by precedence, then the
	// project rules that select the workload, then baseline - plus the egress
	// planes. VM scope resolves pod selectors against the workload's labels; a
	// namespace scope can't, so those bindings arrive marked conditional.
	let { namespace, vm }: { namespace: string; vm?: string } = $props();

	// Keyed on the scope; resource's stale guard drops a response once it moves.
	const key = $derived(`${namespace}/${vm ?? ''}`);
	const effRes = resource<EffectivePolicy>(
		() => key,
		() => (vm ? api.vmPolicy(namespace, vm) : api.namespacePolicy(namespace)),
		{ reset: true },
	);
	const eff = $derived(effRes.data);
	const error = $derived(effRes.failed ? friendlyError(effRes.error) : '');

	const keyOf = (b: PolicyBinding) =>
		`${b.policy.backing}:${b.policy.namespace ?? ''}:${b.policy.name}`;

	const tierLabel = (b: PolicyBinding) =>
		b.policy.kind === 'admin'
			? `${policyKindLabel('admin')}, priority ${b.policy.priority ?? 0}`
			: policyKindLabel(b.policy.kind);

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
				<span
					class="rounded-full bg-accent-soft px-1.5 py-0.5 text-xs whitespace-nowrap text-accent-ink"
					>{tier}</span
				>
			{/if}
			<span class="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{b.policy.name}</span>
			<span
				class="hidden max-w-56 truncate text-xs text-ink-muted sm:inline"
				title={b.policy.applies ?? b.policy.target}>{b.policy.applies ?? b.policy.target}</span
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
					? ': the VMs it selects accept no other incoming traffic'
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
					Matched against {eff.labelsLive ? 'the pod labels' : 'the manifest labels'}:
				</span>
				{#if eff.labels && Object.keys(eff.labels).length}
					{#each Object.entries(eff.labels) as [k, v] (k)}
						<span class="rounded bg-inset px-1.5 py-0.5 text-ink-muted">{k}={v}</span>
					{/each}
				{:else}
					<span class="text-ink-faint"
						>none, so only rules without a label selector can pick it</span
					>
				{/if}
			{:else}
				<span>
					Namespace-wide view: rules targeting specific pods are marked "matching pods only".
				</span>
			{/if}
		</div>

		<section class="rounded border border-line bg-panel">
			<header class="flex items-center gap-2 border-b border-line px-3 py-2">
				<h2 class="text-sm font-semibold text-ink">Between VMs</h2>
				<span class="min-w-0 flex-1 truncate text-xs text-ink-faint"
					>Met top to bottom: cluster rules first, the cluster default rule last</span
				>
			</header>
			{#if denyText}
				<p class="border-b border-line-soft bg-warn-soft/40 px-3 py-2 text-xs text-ink-soft">
					{denyText}
				</p>
			{/if}
			{#if !eff.eastWest?.length}
				<p class="px-3 py-3 text-xs text-ink-faint">
					No rule binds this workload: traffic between VMs is unrestricted.
				</p>
			{:else}
				{#each eff.eastWest as b (keyOf(b))}
					{@render bindingRow(b, tierLabel(b))}
				{/each}
			{/if}
		</section>

		{@render plane(
			'Outbound rules',
			'Traffic leaving the cluster; the first match wins',
			eff.gateway,
			'No outbound rule: it reaches anything outside.',
		)}
		{@render plane(
			'Fixed outbound IP',
			'The source address this workload leaves with',
			eff.snat,
			"None: traffic leaves with its host's address.",
		)}
		{@render plane(
			'External routes',
			'Next hops its outbound traffic takes',
			eff.routes,
			'None: outbound traffic follows the default gateway.',
		)}
	</div>
{/if}
