<script lang="ts">
	import { Activity, ArrowUpRight, Ban, Plus, Search, ShieldCheck } from '@lucide/svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Policy } from '#lib/api.js';
	import {
		canAdoptPolicy,
		canAuthorRef,
		canEditPolicy,
		openAdopt,
		openDelete,
		openEdit,
		policyRef,
	} from '#lib/objects.js';
	import { tierOf } from '#lib/rulelines.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import FileHistory from '#lib/components/FileHistory.svelte';
	import SelectInput from '#lib/components/SelectInput.svelte';
	import SyncBadge from '#lib/components/SyncBadge.svelte';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';
	import Pager from '#lib/components/Pager.svelte';

	// The Firewall tab: every rule as sentences, cluster rules first (they are
	// met first, by priority), then project rules, then what happens at the
	// cluster edge. A live read plane: edits go through the same dialogs and
	// pull requests as everywhere else.
	let tenant = $derived(page.url.searchParams.get('tenant') ?? '');
	function setTenant(v: string) {
		const u = new URL(page.url.href);
		if (v) u.searchParams.set('tenant', v);
		else u.searchParams.delete('tenant');
		goto(u.pathname + u.search, { replace: true, reset: false });
		tenant = v;
	}
	let query = $state(page.url.searchParams.get('q') ?? '');
	let projectPage = $state(1);
	let size = $state(DEFAULT_PAGE_SIZE);
	const q = $derived(query.trim().toLowerCase());

	const tenantNS = $derived(
		new Set(
			inventory.projects.find((p) => p.name === tenant)?.namespaces.map((n) => n.namespace) ?? [],
		),
	);
	// A cluster rule is hidden only when it provably pins other namespaces: a
	// label-selector rule may still apply to the tenant, so it stays.
	const matchesTenant = (p: Policy) => {
		if (!tenant) return true;
		if (p.namespace) return inventory.projectOf(p.namespace) === tenant;
		if (!p.namespaces?.length) return true;
		return p.namespaces.some((ns) => tenantNS.has(ns));
	};
	const matchesQuery = (p: Policy) =>
		!q ||
		[
			p.name,
			p.namespace,
			p.applies,
			p.target,
			...(p.rules ?? []).flatMap((r) => [r.text, r.peer, r.ports]),
		].some((s) => s?.toLowerCase().includes(q));
	const keep = (list: Policy[]) => list.filter((p) => matchesTenant(p) && matchesQuery(p));
	const cluster = $derived(keep(networking.clusterRules));
	const project = $derived(keep(networking.projectRules));
	const egress = $derived(keep(networking.egress));
	const slice = $derived(pageSlice(project.length, projectPage, size));
	const projectRows = $derived(project.slice(slice.start, slice.end));

	const keyOf = (p: Policy) => `${p.backing}:${p.namespace ?? ''}:${p.name}`;
	const projectOf = (p: Policy) => (p.namespace ? inventory.projectOf(p.namespace) : 'platform');
	let history = $state<Record<string, boolean>>({});
	const canProjectRules = $derived(inventory.namespaces.length > 0);
</script>

{#snippet policyRow(p: Policy)}
	{@const k = keyOf(p)}
	<li class="flex flex-col gap-1.5 border-b border-line-soft px-card py-3 last:border-b-0">
		<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
			<span class="font-mono text-[13px] font-medium text-ink">{p.name}</span>
			<span class="text-xs text-ink-faint">
				{#if p.kind === 'admin'}priority {p.priority ?? 0}{:else if p.namespace}{projectOf(p)}, {p.namespace}{:else}{tierOf(
						p,
					)}{/if}
			</span>
			{#if p.sync && p.sync !== 'Synced'}<SyncBadge
					sync={p.sync}
					error={p.syncError ?? ''}
					compact
				/>{/if}
			<span class="ml-auto flex items-center gap-3 text-xs">
				{#if p.sourceFile}
					{#if canEditPolicy(p)}
						<button
							type="button"
							class="text-accent-ink hover:underline"
							onclick={() => openEdit(policyRef(p))}>Edit</button
						>
					{/if}
					<button
						type="button"
						class="text-accent-ink hover:underline"
						onclick={() => (history[k] = !history[k])}
						>{history[k] ? 'Hide history' : 'History'}</button
					>
					{#if canEditPolicy(p)}
						<button
							type="button"
							class="text-danger-ink hover:underline"
							onclick={() => openDelete(policyRef(p), p.sourceFile!)}>Delete</button
						>
					{/if}
				{:else if canAdoptPolicy(p)}
					<span class="text-ink-faint">Not in git.</span>
					<button
						type="button"
						class="text-accent-ink hover:underline"
						onclick={() => openAdopt(policyRef(p))}>Adopt into git</button
					>
				{:else}
					<span class="text-ink-faint">Not in git</span>
				{/if}
			</span>
		</div>
		{#if p.applies}<p class="text-xs text-ink-faint">Applies to {p.applies}.</p>{/if}
		<ul class="flex flex-col gap-1">
			{#each p.rules ?? [] as r, i (i)}
				<li class="flex items-start gap-2 text-[13px] leading-5 text-ink-soft">
					<span class="mt-0.5 shrink-0" aria-hidden="true">
						{#if r.action === 'Allow' && r.direction === 'Ingress'}<ShieldCheck
								size={16}
								class="text-ok"
							/>
						{:else if r.action === 'Deny' || r.action === 'Pass'}<Ban
								size={16}
								class="text-ink-faint"
							/>
						{:else}<ArrowUpRight size={16} class="text-ink-faint" />{/if}
					</span>
					<span
						>{r.text ??
							`${r.action} ${r.direction.toLowerCase()} ${r.peer ?? ''} ${r.ports ?? ''}`}</span
					>
				</li>
			{/each}
			{#if p.isolation}
				<li class="flex items-start gap-2 text-[13px] leading-5 text-ink-soft">
					<Ban size={16} class="mt-0.5 shrink-0 text-ink-faint" />{p.isolation}
				</li>
			{/if}
		</ul>
		{#if p.sourceFile && history[k]}
			{@const ref = policyRef(p)}
			<FileHistory
				project={projectOf(p)}
				name={p.name}
				load={() => api.objectHistory(ref.resource, ref.namespace, ref.name)}
				restore={canAuthorRef(ref)
					? (hash) => api.restoreObjectVersion(ref.resource, ref.namespace, ref.name, hash)
					: undefined}
				mine={(it) => it.resource === ref.resource && it.name === ref.name}
			/>
		{/if}
	</li>
{/snippet}

{#snippet empty(text: string)}
	<p class="px-card py-4 text-[13px] text-ink-faint">
		{q || tenant ? 'No rule matches the filter.' : text}
	</p>
{/snippet}

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center gap-2">
		<SelectInput
			value={tenant}
			size="sm"
			width="w-auto"
			onchange={(e) => setTenant(e.currentTarget.value)}
			aria-label="Filter by tenant"
		>
			<option value="">All projects</option>
			{#each inventory.projectNames as name (name)}
				<option value={name}>{name}</option>
			{/each}
		</SelectInput>
		<label
			class="flex h-8 w-80 items-center gap-2 rounded-md border border-ink-faint/80 bg-panel px-2.5 text-ink-muted"
		>
			<Search size={16} />
			<input
				type="text"
				bind:value={query}
				oninput={() => (projectPage = 1)}
				placeholder="Filter by name, port or address"
				aria-label="Filter rules"
				class="min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-0"
			/>
		</label>
		<span class="ml-auto flex flex-wrap gap-2">
			<Button variant="outline" onclick={() => (ui.modal = { kind: 'trace' })}
				><Activity size={16} />Check connectivity</Button
			>
		</span>
	</div>

	<Card title="Cluster rules" meta="Met first, lowest priority first" flush>
		{#snippet actions()}
			{#if inventory.canAdminFw}
				<Button variant="secondary" size="sm" onclick={() => (ui.modal = { kind: 'adminFw' })}
					><Plus size={14} />New cluster rule</Button
				>
			{/if}
		{/snippet}
		{#if cluster.length}
			<ul>
				{#each cluster as p (keyOf(p))}{@render policyRow(p)}{/each}
			</ul>
		{:else}
			{@render empty(
				inventory.canAdminFw ? 'No cluster rules.' : 'No cluster rules visible to you.',
			)}
		{/if}
	</Card>

	<Card title="Project rules" meta="Inside each project, after the cluster rules" flush>
		{#snippet actions()}
			{#if canProjectRules}
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (ui.modal = { kind: 'dfw', namespaces: inventory.namespaces })}
					><Plus size={14} />New project rule</Button
				>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (ui.modal = { kind: 'egressFw', namespaces: inventory.namespaces })}
					><Plus size={14} />New outbound rule</Button
				>
			{/if}
		{/snippet}
		{#if project.length}
			<ul>
				{#each projectRows as p (keyOf(p))}{@render policyRow(p)}{/each}
			</ul>
			<Pager
				total={project.length}
				bind:page={projectPage}
				bind:size
				class="border-t border-line-soft px-card py-2.5"
			/>
		{:else}
			{@render empty('No project rules. Every VM accepts traffic from anywhere.')}
		{/if}
	</Card>

	<Card title="Leaving the cluster" meta="Fixed outbound IPs and external routes" flush>
		{#snippet actions()}
			{#if inventory.canEgress}
				<Button variant="secondary" size="sm" onclick={() => (ui.modal = { kind: 'tier0' })}
					><Plus size={14} />New fixed outbound IP or route</Button
				>
			{/if}
		{/snippet}
		{#if egress.length}
			<ul>
				{#each egress as p (keyOf(p))}{@render policyRow(p)}{/each}
			</ul>
		{:else}
			{@render empty("None. Traffic leaves with its host's address.")}
		{/if}
	</Card>
</div>
