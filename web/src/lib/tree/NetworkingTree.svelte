<script lang="ts">
	import { ChevronDown, ChevronRight, Map as MapIcon } from '@lucide/svelte';
	import { page } from '$app/state';
	import type { Network } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { POD_NETWORK } from '#lib/lenses.js';
	import { cutOffHosts, uplinkLabel } from '#lib/netmap.js';
	import { networkHref, networkingTab, scopeFromPath } from '#lib/nav.js';
	import { networkKey } from '#lib/networks.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';

	// The Networking tree: the map, then networks by kind (project networks
	// grouped by project), the built-in pod network, and the uplinks. Groups
	// show their biggest members and a "N more" row into the Networks tab, so
	// a hundred networks never become a hundred rows. Dots mark problems.
	const TOP = 5;
	const scope = $derived(scopeFromPath(page.url.pathname));
	const current = $derived(scope.kind === 'network' ? scope.network : '');
	const onRoot = $derived(page.url.pathname === '/networking');
	const rootTab = $derived(networkingTab(page.url.searchParams.get('tab')));

	const open = persisted<Record<string, boolean>>('dotvirt.tree.networking', {});
	const toggle = (id: string, now: boolean) => (open.value = { ...open.value, [id]: !now });
	const fmt = (n: number) => n.toLocaleString('en-US');
	const vmsOf = (n: Network) => networking.vmsOn(networkKey(n)).length;
	const byVMs = (a: Network, b: Network) => vmsOf(b) - vmsOf(a) || a.name.localeCompare(b.name);
	const overlapIn = (nets: Network[]) =>
		nets.some((n) => networking.problemsOf(networkKey(n)).length);
	const cutIn = (nets: Network[]) => nets.some((n) => cutOffHosts(n, inventory.uplinks).length);

	const owned = $derived(
		inventory.projects
			.map((p) => ({
				name: p.name,
				nets: networking.netsOfProject(p).sort(byVMs),
				vms: p.namespaces.reduce((s, n) => s + n.vms.length, 0),
			}))
			.filter((p) => p.nets.length)
			.sort((a, b) => b.vms - a.vms || a.name.localeCompare(b.name)),
	);
	const currentNet = $derived(current ? networking.byKey.get(current) : undefined);
	const currentProject = $derived(currentNet ? networking.ownerOf(currentNet) : '');
	const shownProjects = $derived.by(() => {
		const top = owned.slice(0, TOP);
		const cur = owned.find((p) => p.name === currentProject);
		return cur && !top.includes(cur) ? [...top, cur] : top;
	});
	const restProjects = $derived(owned.filter((p) => !shownProjects.includes(p)));
	const shared = $derived([...networking.sharedNets].sort(byVMs));
	const vlans = $derived([...networking.vlanNets].sort(byVMs));

	// A group holding the open network starts expanded.
	const isOpen = (id: string, holds: boolean) => open.value[id] ?? holds;
	const holds = (nets: Network[]) => !!current && nets.some((n) => networkKey(n) === current);
	const failing = $derived(inventory.uplinks.filter((u) => u.failures?.length));
	const pex = $derived(isOpen('project', !!currentProject));
	const restNets = $derived(restProjects.flatMap((p) => p.nets));
	const upEx = $derived(isOpen('uplinks', false));
</script>

{#snippet row(opts: {
	href?: string;
	label: string;
	count?: string;
	indent?: number;
	expanded?: boolean;
	ontoggle?: () => void;
	active?: boolean;
	mono?: boolean;
	dot?: 'warn' | 'danger' | null;
	spacer?: boolean;
})}
	<div
		class="flex w-full items-center gap-1.5 rounded py-1.5 pr-2 text-[13px] leading-5 {opts.active
			? 'bg-side-hover text-side-ink'
			: 'text-side-dim hover:bg-side-hover/60 hover:text-side-ink'}"
		style="padding-left: {8 + (opts.indent ?? 0)}px"
	>
		{#if opts.expanded !== undefined}
			<button
				type="button"
				aria-expanded={opts.expanded}
				aria-label="{opts.expanded ? 'Collapse' : 'Expand'} {opts.label}"
				onclick={opts.ontoggle}
				class="flex shrink-0 items-center"
				>{#if opts.expanded}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}</button
			>
		{:else if opts.spacer}
			<span class="w-3.5 shrink-0"></span>
		{/if}
		{#if opts.href}
			<a
				href={opts.href}
				aria-current={opts.active ? 'page' : undefined}
				class="flex min-w-0 flex-1 items-center gap-1.5"
			>
				<span class="truncate {opts.mono ? 'font-mono' : ''}">{opts.label}</span>
				{#if opts.dot}<span
						class="size-[7px] shrink-0 rounded-full {opts.dot === 'danger'
							? 'bg-danger'
							: 'bg-warn'}"
					></span><span class="sr-only">, has a problem</span>{/if}
				{#if opts.count}<span class="ml-auto shrink-0 text-xs text-side-dim">{opts.count}</span
					>{/if}
			</a>
		{:else}
			<button
				type="button"
				onclick={opts.ontoggle}
				class="flex min-w-0 flex-1 items-center gap-1.5 text-left"
			>
				<span class="truncate">{opts.label}</span>
				{#if opts.dot}<span
						class="size-[7px] shrink-0 rounded-full {opts.dot === 'danger'
							? 'bg-danger'
							: 'bg-warn'}"
					></span><span class="sr-only">, has a problem</span>{/if}
				{#if opts.count}<span class="ml-auto shrink-0 text-xs text-side-dim">{opts.count}</span
					>{/if}
			</button>
		{/if}
	</div>
{/snippet}

{#snippet label(text: string, count?: string)}
	<div
		class="flex justify-between px-2 pt-2.5 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase"
	>
		<span>{text}</span>{#if count}<span>{count}</span>{/if}
	</div>
{/snippet}

{#snippet leaf(n: Network, indent: number)}
	{@const k = networkKey(n)}
	{@render row({
		href: networkHref(k),
		label: n.name,
		count: plural(vmsOf(n), 'VM'),
		indent,
		active: current === k,
		mono: true,
		dot: cutOffHosts(n, inventory.uplinks).length
			? 'danger'
			: networking.problemsOf(k).length
				? 'warn'
				: null,
	})}
{/snippet}

{#snippet group(id: string, title: string, nets: Network[], kind: string)}
	{@const ex = isOpen(id, holds(nets))}
	{@render row({
		label: title,
		count: fmt(nets.length),
		expanded: ex,
		ontoggle: () => toggle(id, ex),
		dot: cutIn(nets) ? 'danger' : overlapIn(nets) ? 'warn' : null,
	})}
	{#if ex}
		{#each nets.slice(0, 8) as n (networkKey(n))}{@render leaf(n, 34)}{/each}
		{#if nets.length > 8}
			{@render row({
				href: `/networking?tab=networks&kind=${kind}`,
				label: `${nets.length - 8} more`,
				indent: 34,
			})}
		{/if}
	{/if}
{/snippet}

<nav class="flex flex-col gap-0.5 px-2.5 py-2 select-none" aria-label="Networking">
	<a
		href="/networking"
		aria-current={onRoot && rootTab === 'map' ? 'page' : undefined}
		class="flex items-center gap-1.5 rounded px-2 py-1.5 text-[13px] leading-5 {onRoot &&
		rootTab === 'map'
			? 'bg-side-hover text-side-ink'
			: 'text-side-dim hover:bg-side-hover/60 hover:text-side-ink'}"><MapIcon size={14} />Map</a
	>

	{@render label('Networks', fmt(inventory.networks.length))}
	{@render row({
		label: 'Project networks',
		count: fmt(networking.projectNets.length),
		expanded: pex,
		ontoggle: () => toggle('project', pex),
		dot: overlapIn(networking.projectNets) ? 'warn' : null,
	})}
	{#if pex}
		{#each shownProjects as p (p.name)}
			{@const id = `p:${p.name}`}
			{@const ex = isOpen(id, p.name === currentProject)}
			{@render row({
				label: p.name,
				count: fmt(p.nets.length),
				indent: 14,
				expanded: ex,
				ontoggle: () => toggle(id, ex),
				dot: overlapIn(p.nets) ? 'warn' : null,
			})}
			{#if ex}
				{#each p.nets as n (networkKey(n))}{@render leaf(n, 48)}{/each}
			{/if}
		{/each}
		{#if restProjects.length}
			{@render row({
				href: '/networking?tab=networks&kind=project',
				label: `${fmt(restProjects.length)} more project${restProjects.length === 1 ? '' : 's'}`,
				count: fmt(restNets.length),
				indent: 34,
				dot: overlapIn(restNets) ? 'warn' : null,
			})}
		{/if}
	{/if}
	{@render group('shared', 'Shared networks', shared, 'shared')}
	{@render group('vlan', 'VLAN networks', vlans, 'vlan')}
	{@render row({
		href: networkHref(POD_NETWORK),
		label: 'Pod network',
		count: 'built in',
		spacer: true,
		active: current === POD_NETWORK,
	})}

	{#if inventory.uplinks.length}
		{@render label('Platform')}
		{@render row({
			label: 'Uplinks',
			count: fmt(inventory.uplinks.length),
			expanded: upEx,
			ontoggle: () => toggle('uplinks', upEx),
			dot: failing.length ? 'danger' : null,
		})}
		{#if upEx}
			{#each inventory.uplinks as u (u.name)}
				{@render row({
					href: `/networking?tab=uplinks`,
					label: uplinkLabel(u),
					count: plural(u.nodeCount, 'host'),
					indent: 34,
					mono: true,
					dot: u.failures?.length ? 'danger' : null,
				})}
			{/each}
		{/if}
	{/if}
</nav>
