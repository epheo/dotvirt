<script lang="ts">
	import { Search } from '@lucide/svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import type { Network } from '#lib/api.js';
	import { cutOffHosts } from '#lib/netmap.js';
	import { networkHref } from '#lib/nav.js';
	import { networkKey, networkKind } from '#lib/networks.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';
	import Pager from '#lib/components/Pager.svelte';

	// Every network, paged: kind chips with counts, a text filter over name,
	// subnet and owner, and ?kind= / ?project= so the tree and the map can
	// deep-link a slice.
	const kind = $derived(page.url.searchParams.get('kind') ?? 'all');
	const project = $derived(page.url.searchParams.get('project') ?? '');
	let query = $state('');
	let pageNo = $state(1);
	let size = $state(DEFAULT_PAGE_SIZE);

	type Row = {
		n: Network;
		key: string;
		vms: number;
		owner: string;
		cut: string[];
		problem: boolean;
	};
	const rows = $derived.by((): Row[] =>
		inventory.networks
			.map((n) => {
				const key = networkKey(n);
				const cut = cutOffHosts(n, inventory.uplinks);
				return {
					n,
					key,
					vms: networking.vmsOn(key).length,
					owner: networking.ownerOf(n),
					cut,
					problem: cut.length > 0 || networking.problemsOf(key).length > 0,
				};
			})
			.sort((a, b) => b.vms - a.vms || a.n.name.localeCompare(b.n.name)),
	);
	const KINDS = [
		{ id: 'all', label: 'All', test: () => true },
		{ id: 'project', label: 'Project', test: (r: Row) => networkKind(r.n) === 'Project network' },
		{ id: 'shared', label: 'Shared', test: (r: Row) => networkKind(r.n) === 'Shared network' },
		{ id: 'vlan', label: 'VLAN', test: (r: Row) => networkKind(r.n) === 'VLAN network' },
		{ id: 'problems', label: 'Problems', test: (r: Row) => r.problem },
	];
	const q = $derived(query.trim().toLowerCase());
	const shown = $derived(
		rows.filter(
			(r) =>
				KINDS.find((k) => k.id === kind)!.test(r) &&
				(!project || r.owner === project) &&
				(!q ||
					r.n.name.toLowerCase().includes(q) ||
					r.owner.toLowerCase().includes(q) ||
					(r.n.subnets ?? []).some((s) => s.startsWith(q))),
		),
	);
	const slice = $derived(pageSlice(shown.length, pageNo, size));
	const pageRows = $derived(shown.slice(slice.start, slice.end));
	function setKind(id: string) {
		const u = new URL(page.url.href);
		if (id === 'all') u.searchParams.delete('kind');
		else u.searchParams.set('kind', id);
		u.searchParams.delete('project');
		pageNo = 1;
		goto(u.pathname + u.search, { replace: true, reset: false });
	}
	const ownerText = (r: Row) => {
		if (r.n.scope !== 'shared') return r.owner || r.n.namespace || '';
		const n = networking.projectsOf(r.n).length;
		return `${n} project${n === 1 ? '' : 's'}`;
	};
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center gap-2">
		{#each KINDS as k (k.id)}
			{@const n = rows.filter(k.test).length}
			<button
				type="button"
				aria-pressed={kind === k.id}
				onclick={() => setKind(k.id)}
				class="inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-[13px] {kind ===
				k.id
					? 'border-accent bg-select-soft font-medium text-accent-ink'
					: 'border-line bg-panel text-ink hover:border-ink-faint'}"
				>{k.label}<span
					class={k.id === 'problems' && n ? 'font-bold text-danger-ink' : 'text-ink-faint'}
					>{n}</span
				></button
			>
		{/each}
		{#if project}
			<span class="text-[13px] text-ink-faint">in {project}</span>
		{/if}
		<label
			class="ml-auto flex h-8 w-72 items-center gap-2 rounded-md border border-ink-faint/80 bg-panel px-2.5 text-ink-muted"
		>
			<Search size={16} />
			<input
				type="text"
				bind:value={query}
				oninput={() => (pageNo = 1)}
				placeholder="Filter by name, subnet or project"
				aria-label="Filter networks"
				class="min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-0"
			/>
		</label>
	</div>

	<Card title="Networks" meta="{shown.length} of {rows.length}" flush>
		<div class="overflow-x-auto">
			<table class="w-full text-[13px]">
				<thead>
					<tr class="border-b border-line-soft text-left text-xs text-ink-faint">
						<th class="py-2 pl-card font-medium">Name</th>
						<th class="py-2 font-medium">Kind</th>
						<th class="py-2 font-medium">Owner</th>
						<th class="py-2 font-medium">Subnet</th>
						<th class="py-2 text-right font-medium">VMs</th>
						<th class="py-2 pl-6 font-medium">Uplink</th>
						<th class="py-2 pr-card font-medium">State</th>
					</tr>
				</thead>
				<tbody>
					{#each pageRows as r (r.key)}
						<tr class="border-b border-line-soft last:border-b-0 hover:bg-select-soft">
							<td class="py-2 pl-card"
								><a
									href={networkHref(r.key)}
									class="font-mono font-medium text-accent-ink hover:underline">{r.n.name}</a
								></td
							>
							<td class="py-2 text-ink-soft">{networkKind(r.n)}</td>
							<td class="py-2 text-ink-soft">{ownerText(r)}</td>
							<td class="py-2 font-mono text-xs text-ink-soft">{r.n.subnets?.join(', ') || '-'}</td>
							<td class="py-2 text-right tabular-nums">{r.vms.toLocaleString('en-US')}</td>
							<td class="py-2 pl-6 text-ink-soft"
								>{r.n.kind === 'vlan'
									? `${r.n.uplink ?? '-'}${r.n.vlan ? `, VLAN ${r.n.vlan}` : ''}`
									: 'default'}</td
							>
							<td class="py-2 pr-card">
								{#if r.cut.length}
									<span class="text-danger-ink">Cut off on {r.cut.join(', ')}</span>
								{:else if networking.problemsOf(r.key).length}
									<span class="text-warn-ink">Overlap</span>
								{:else if r.n.sync === 'OutOfSync'}
									<span class="text-warn-ink">Differs from git</span>
								{:else}
									<span class="text-ink-faint">{r.n.sourceFile ? 'In git' : 'Not in git'}</span>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<Pager
			total={shown.length}
			bind:page={pageNo}
			bind:size
			class="border-t border-line-soft px-card py-2.5"
		/>
	</Card>
</div>
