<script lang="ts">
	import { Funnel, Maximize, Search, X, ZoomIn, ZoomOut } from '@lucide/svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { vmKey, type VM } from '#lib/api.js';
	import { clusterMap, searchMap, wayOut, type MapFocus } from '#lib/netmap.js';
	import { networkKey } from '#lib/networks.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import ClusterMap from './ClusterMap.svelte';
	import MapInspector from './MapInspector.svelte';

	// The Map tab: find a VM, a network or an address, light its way out, and
	// inspect whatever is selected. The selection and the problems filter ride
	// the URL (?sel=, ?problems=1) so a view is shareable; the find box starts
	// from ?find= ("Show on map" lands here with it).
	let find = $state(page.url.searchParams.get('find') ?? '');
	let netBudget = $state(6);
	let projectBudget = $state(5);
	let zoom = $state(1);
	let boxWidth = $state(0);

	const problemsOnly = $derived(page.url.searchParams.get('problems') === '1');
	const search = $derived(searchMap(find, inventory.allVMs, inventory.networks, 8));

	// The selection: an explicit pick, else the one thing the search names.
	const autoSel = $derived.by(() => {
		if (!search) return '';
		const q = find.trim().toLowerCase();
		const vm = search.vms.find((v) => v.name.toLowerCase() === q);
		if (vm) return `vm:${vmKey(vm)}`;
		const net = search.networks.find((n) => n.name.toLowerCase() === q);
		if (net) return `net:${networkKey(net)}`;
		if (search.total !== 1) return '';
		return search.vms[0] ? `vm:${vmKey(search.vms[0])}` : `net:${networkKey(search.networks[0])}`;
	});
	const sel = $derived(page.url.searchParams.get('sel') ?? autoSel);

	function setParams(set: Record<string, string | null>) {
		const u = new URL(page.url.href);
		for (const [k, v] of Object.entries(set))
			if (v) u.searchParams.set(k, v);
			else u.searchParams.delete(k);
		goto(u.pathname + u.search, { replace: true, reset: false });
	}
	const select = (s: string) => setParams({ sel: s === sel ? null : s });
	function typed() {
		if (page.url.searchParams.has('sel')) setParams({ sel: null });
	}

	const selVM = $derived.by((): VM | null => {
		if (!sel.startsWith('vm:')) return null;
		const [ns, name] = sel.slice(3).split('/');
		return inventory.findVM(ns, name) ?? null;
	});
	const target = $derived.by(() => {
		if (selVM) return { kind: 'vm' as const, vm: selVM };
		if (sel.startsWith('net:')) {
			const net = networking.byKey.get(sel.slice(4));
			return net ? { kind: 'network' as const, net } : null;
		}
		if (sel.startsWith('up:')) {
			const name = sel.slice(3);
			const uplink = inventory.uplinks.find((u) => u.name === name);
			if (!uplink) return null;
			const problem =
				networking.problems.find((p) => p.kind === 'uplink' && p.uplink === name) ?? null;
			return { kind: 'uplink' as const, uplink, problem };
		}
		if (sel.startsWith('proj:')) return { kind: 'project' as const, name: sel.slice(5) };
		if (sel === 'edge' || sel === 'fabric' || sel === 'pod')
			return { kind: sel as 'edge' | 'fabric' | 'pod' };
		return null;
	});
	const focus = $derived.by((): MapFocus | null => {
		if (selVM) return { kind: 'vm', vm: selVM };
		if (sel.startsWith('net:')) return { kind: 'network', key: sel.slice(4) };
		if (sel.startsWith('up:')) return { kind: 'uplink', name: sel.slice(3) };
		if (sel.startsWith('proj:')) return { kind: 'project', name: sel.slice(5) };
		return null;
	});

	const model = $derived(
		clusterMap({
			networks: inventory.networks,
			uplinks: inventory.uplinks,
			projects: inventory.projects,
			policies: inventory.policies,
			members: networking.members,
			problems: networking.problems,
			focus,
			problemsOnly,
			netBudget,
			projectBudget,
		}),
	);
	const way = $derived(selVM ? wayOut(selVM, inventory.networks, inventory.uplinks) : null);
	const hit = $derived(selVM ? { vm: selVM, project: inventory.projectOf(selVM.namespace) } : null);
	const showList = $derived(!!search && search.total > 1 && !sel);
	const fmt = (n: number) => n.toLocaleString('en-US');
	const fit = () => (zoom = Math.min(1, Math.max(0.5, (boxWidth - 32) / 744)));
</script>

<div class="flex flex-col gap-4">
	<div class="flex flex-wrap items-center gap-x-3 gap-y-2.5">
		<div class="relative w-[360px] max-w-full">
			<div
				class="flex h-9 items-center gap-2 rounded-md border border-ink-faint/80 bg-panel pr-1 pl-2.5 text-ink-muted focus-within:outline-2 focus-within:-outline-offset-1 focus-within:outline-accent"
			>
				<Search size={16} class="shrink-0" />
				<input
					type="text"
					bind:value={find}
					oninput={typed}
					aria-label="Find a VM, network or IP"
					placeholder="Find a VM, network or IP"
					class="min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-0"
				/>
				{#if search}
					<span class="text-xs whitespace-nowrap text-ink-muted">{search.summary}</span>
					<button
						type="button"
						aria-label="Clear search"
						onclick={() => {
							find = '';
							setParams({ sel: null, find: null });
						}}
						class="inline-flex size-7 items-center justify-center rounded-md text-ink-soft hover:bg-inset-strong"
						><X size={16} /></button
					>
				{/if}
			</div>
			{#if showList && search}
				<ul
					class="absolute top-10 left-0 z-20 w-full overflow-hidden rounded-md border border-line bg-panel py-1 text-[13px] shadow-lg"
				>
					{#each search.vms as v (vmKey(v))}
						<li>
							<button
								type="button"
								onclick={() => select(`vm:${vmKey(v)}`)}
								class="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-select-soft"
								><span class="font-mono font-medium">{v.name}</span><span class="text-ink-faint"
									>VM in {v.namespace}</span
								>{#if v.guestIP}<span class="ml-auto font-mono text-xs text-ink-faint"
										>{v.guestIP}</span
									>{/if}</button
							>
						</li>
					{/each}
					{#each search.networks as n (networkKey(n))}
						<li>
							<button
								type="button"
								onclick={() => select(`net:${networkKey(n)}`)}
								class="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-select-soft"
								><span class="font-mono font-medium">{n.name}</span><span class="text-ink-faint"
									>network</span
								>{#if n.subnets?.length}<span class="ml-auto font-mono text-xs text-ink-faint"
										>{n.subnets[0]}</span
									>{/if}</button
							>
						</li>
					{/each}
					{#if search.total > search.vms.length + search.networks.length}
						<li class="px-3 py-1.5 text-xs text-ink-faint">
							{fmt(search.total - search.vms.length - search.networks.length)} more. Type more of the
							name.
						</li>
					{/if}
				</ul>
			{/if}
		</div>
		<button
			type="button"
			aria-pressed={problemsOnly}
			onclick={() => setParams({ problems: problemsOnly ? null : '1' })}
			class="inline-flex h-8 items-center gap-1.5 rounded-md border px-3.5 text-sm font-medium {problemsOnly
				? 'border-accent bg-select-soft text-accent-ink'
				: 'border-line bg-panel text-ink hover:border-ink-faint'}"
		>
			<Funnel size={16} />Problems only
			<span
				class="inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-full px-[5px] text-[11px] font-bold {networking
					.problems.length
					? 'bg-danger-soft text-danger-ink'
					: 'bg-inset-strong text-ink'}">{networking.problems.length}</span
			>
		</button>
		<span class="ml-auto text-[13px] text-ink-faint">
			Showing {model.shown.networks} of {fmt(model.shown.networksTotal)} networks and {model.shown
				.projects} of {fmt(model.shown.projectsTotal)} projects.
		</span>
	</div>

	<div class="flex flex-wrap items-start gap-4">
		<Card
			title="Cluster map"
			meta="Select anything to inspect it. Expand a group for more."
			flush
			class="min-w-0 flex-[1_1_640px]"
		>
			{#snippet actions()}
				<span role="group" aria-label="Zoom" class="flex gap-0.5">
					<button
						type="button"
						aria-label="Zoom in"
						onclick={() => (zoom = Math.min(1.4, +(zoom + 0.1).toFixed(1)))}
						class="inline-flex size-7 items-center justify-center rounded-md text-ink-soft hover:bg-inset-strong"
						><ZoomIn size={16} /></button
					>
					<button
						type="button"
						aria-label="Zoom out"
						onclick={() => (zoom = Math.max(0.5, +(zoom - 0.1).toFixed(1)))}
						class="inline-flex size-7 items-center justify-center rounded-md text-ink-soft hover:bg-inset-strong"
						><ZoomOut size={16} /></button
					>
					<button
						type="button"
						aria-label="Fit to view"
						onclick={fit}
						class="inline-flex size-7 items-center justify-center rounded-md text-ink-soft hover:bg-inset-strong"
						><Maximize size={16} /></button
					>
				</span>
			{/snippet}
			<div class="overflow-x-auto p-4" bind:clientWidth={boxWidth}>
				<ClusterMap
					{model}
					{sel}
					{way}
					{hit}
					{zoom}
					onselect={select}
					onmorenets={() => (netBudget += 10)}
					onmoreprojects={() => (projectBudget += 10)}
				/>
			</div>
			<div
				class="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-line-soft px-card py-2.5 text-xs leading-4 text-ink-soft"
			>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-[3px] border-accent"></span>{selVM
						? `${selVM.name}'s way out`
						: 'Way out'}</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-2 border-wire"></span>Attached</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-2 border-dashed border-danger"
					></span>Failing</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span
						class="inline-flex h-4 items-center rounded-full border border-danger/40 bg-danger-soft px-1.5 text-[11px] font-medium text-danger-ink"
						>!</span
					>Problem, with its fix</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block h-3 w-4 rounded-[3px] border border-dashed border-ink-faint/60"
					></span>Group, expand for more</span
				>
				<span class="inline-flex items-center gap-1.5"
					><b>{fmt(model.projects[0]?.vms ?? 0)}</b>Numbers are VMs</span
				>
			</div>
		</Card>

		{#if target}
			<MapInspector
				{target}
				onclose={() => {
					find = '';
					setParams({ sel: null, find: null });
				}}
			/>
		{/if}
	</div>
</div>
