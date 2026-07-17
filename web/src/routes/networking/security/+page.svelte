<script lang="ts">
	import { ChevronDown, ChevronRight, Plus } from 'lucide-svelte';
	import type { Policy, PolicyKind } from '$lib/api';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import PolicyRuleTable from '$lib/components/PolicyRuleTable.svelte';
	import SyncBadge from '$lib/components/SyncBadge.svelte';

	// The Security view: the live policy plane in NSX-T tiers — cluster admin DFW
	// rules above, project DFW and gateway-firewall rules per namespace, Tier-0
	// (SNAT + external routes) below. Read plane only: every row is a live object;
	// authoring goes through the same modals (and PRs) as everywhere else.
	const policies = $derived(inventory.policies);
	const caps = $derived(inventory.caps);

	// Scope filters: a tenant (project) and a free-text query, combined. The
	// query also matches rule contents (peer, ports, action) so "where is
	// TCP/22 allowed" is answerable from here.
	let tenant = $state('');
	let query = $state('');
	const q = $derived(query.trim().toLowerCase());
	const filtered = $derived(tenant !== '' || q !== '');

	const tenantNS = $derived(
		new Set(
			(inventory.inventory?.projects ?? [])
				.find((p) => p.name === tenant)
				?.namespaces.map((n) => n.namespace) ?? [],
		),
	);

	// Cluster-tier rows are hidden only when they provably pin other namespaces
	// (p.namespaces, from the enumerable selector). A label-selector admin rule
	// may still apply to the tenant, so it stays — never hide a maybe-applying
	// firewall rule.
	const matchesTenant = (p: Policy): boolean => {
		if (!tenant) return true;
		if (p.namespace) return inventory.projectOf(p.namespace) === tenant;
		if (!p.namespaces?.length) return true;
		return p.namespaces.some((ns) => tenantNS.has(ns));
	};
	const matchesQuery = (p: Policy): boolean => {
		if (!q) return true;
		return [
			p.name,
			p.namespace,
			p.target,
			p.backing,
			...(p.rules ?? []).flatMap((r) => [r.action, r.peer, r.ports]),
		].some((s) => s?.toLowerCase().includes(q));
	};
	const shown = $derived(policies.filter((p) => matchesTenant(p) && matchesQuery(p)));

	let expanded = $state<Record<string, boolean>>({});
	const keyOf = (p: Policy) => `${p.backing}:${p.namespace ?? ''}:${p.name}`;

	// New-policy buttons open the same modals the header/context menus do; each is
	// gated exactly like its entry point there.
	const canProjectRules = $derived(inventory.namespaces.length > 0);
	const canTier0 = $derived(!!caps?.egressIP || !!caps?.externalRoute);
</script>

<Breadcrumb trail={[{ label: 'Networking', href: '/networking' }, { label: 'Security' }]} />

<div class="flex flex-wrap items-center gap-2 border-b border-line bg-panel px-4 py-2">
	<select
		bind:value={tenant}
		aria-label="Filter by tenant"
		class="rounded border border-line-strong px-2 py-1 text-xs"
	>
		<option value="">All tenants</option>
		{#each inventory.projectNames as name (name)}
			<option value={name}>{name}</option>
		{/each}
	</select>
	<input
		type="search"
		bind:value={query}
		aria-label="Filter policies"
		placeholder="Filter by name, target, or rule"
		class="w-64 rounded border border-line-strong px-2 py-1 text-xs"
	/>
	{#if filtered}
		<span class="text-xs text-ink-faint">{shown.length} of {policies.length} policies</span>
		<button
			type="button"
			onclick={() => ((tenant = ''), (query = ''))}
			class="text-xs text-accent hover:underline">Clear</button
		>
	{/if}
</div>

<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
	{#snippet policyRows(list: Policy[], emptyHint: string)}
		{#if list.length === 0}
			<div class="px-3 py-4 text-center text-xs text-ink-faint">
				{filtered ? 'No policies match the filter.' : emptyHint}
			</div>
		{:else}
			<div class="divide-y divide-line-soft">
				{#each list as p (keyOf(p))}
					<!-- A text match may live in a collapsed rule row, so searching
					     auto-opens matches; a click still overrides. -->
					{@const open = expanded[keyOf(p)] ?? q !== ''}
					<div>
						<button
							type="button"
							class="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-inset"
							onclick={() => (expanded[keyOf(p)] = !open)}
						>
							{#if open}<ChevronDown size={14} class="shrink-0 text-ink-faint" />
							{:else}<ChevronRight size={14} class="shrink-0 text-ink-faint" />{/if}
							<span class="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{p.name}</span>
							{#if p.namespace}
								<span class="rounded bg-inset px-1.5 py-0.5 text-xs text-ink-muted"
									>{p.namespace}</span
								>
							{/if}
							{#if p.kind === 'admin'}
								<span class="text-xs text-ink-faint" title="Precedence — lower wins"
									>priority {p.priority ?? 0}</span
								>
							{:else if p.kind === 'baseline'}
								<span class="text-xs text-ink-faint">baseline</span>
							{/if}
							<span
								class="hidden max-w-56 truncate text-xs text-ink-muted sm:inline"
								title={p.target}>{p.target}</span
							>
							<span class="text-xs whitespace-nowrap text-ink-faint">
								{p.rules?.length ?? 0}
								{(p.rules?.length ?? 0) === 1 ? 'rule' : 'rules'}
							</span>
							{#if p.sync}<SyncBadge sync={p.sync} error={p.syncError ?? ''} compact />{/if}
						</button>
						{#if open}
							<div class="bg-inset/40 px-3 pb-3 pl-9">
								{#if !p.rules?.length}
									<p class="pt-2 text-xs text-ink-faint">
										No rules{p.kind === 'dfw'
											? ' — the selected pods default-deny all other ingress'
											: ''}.
									</p>
								{:else}
									<PolicyRuleTable rules={p.rules} />
								{/if}
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/if}
	{/snippet}

	{#snippet section(
		title: string,
		hint: string,
		kinds: PolicyKind[],
		emptyHint: string,
		onnew: (() => void) | null,
	)}
		{@const list = shown.filter((p) => kinds.includes(p.kind))}
		{@const total = policies.filter((p) => kinds.includes(p.kind)).length}
		<section class="rounded border border-line bg-panel">
			<header class="flex items-center gap-2 border-b border-line px-3 py-2">
				<h2 class="text-sm font-semibold text-ink">{title}</h2>
				<span class="rounded bg-inset px-1.5 py-0.5 text-xs text-ink-muted">
					{list.length === total ? total : `${list.length} of ${total}`}
				</span>
				<span class="min-w-0 flex-1 truncate text-xs text-ink-faint">{hint}</span>
				{#if onnew}
					<button
						type="button"
						onclick={onnew}
						class="inline-flex items-center gap-1 rounded border border-line px-2 py-1 text-xs font-medium text-ink-soft hover:bg-inset"
					>
						<Plus size={12} /> New
					</button>
				{/if}
			</header>
			{@render policyRows(list, emptyHint)}
		</section>
	{/snippet}

	{@render section(
		'Distributed Firewall — admin rules',
		'Cluster-wide, priority-ordered; override or backstop every project policy',
		['admin', 'baseline'],
		caps?.adminNetworkPolicy
			? 'No admin policies.'
			: 'No admin policies visible (platform authority required).',
		caps?.adminNetworkPolicy ? () => (ui.modal = { kind: 'adminFw' }) : null,
	)}

	{@render section(
		'Distributed Firewall — project rules',
		'East-west NetworkPolicies inside each project',
		['dfw'],
		'No project firewall rules.',
		canProjectRules ? () => (ui.modal = { kind: 'dfw', namespaces: inventory.namespaces }) : null,
	)}

	{@render section(
		'Gateway Firewall',
		'North-south egress per project (first match wins)',
		['gateway'],
		'No egress firewalls.',
		canProjectRules
			? () => (ui.modal = { kind: 'egressFw', namespaces: inventory.namespaces })
			: null,
	)}

	{@render section(
		'Tier-0',
		'Egress SNAT pools and policy-based external routes',
		['egressip', 'route'],
		canTier0 ? 'No Tier-0 policies.' : 'No Tier-0 policies visible (platform authority required).',
		canTier0 ? () => (ui.modal = { kind: 'tier0' }) : null,
	)}
</div>
