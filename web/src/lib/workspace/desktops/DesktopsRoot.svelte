<script lang="ts">
	import { page } from '$app/state';
	import { allDesktops, joinClaims, poolState, unlistedLines } from '#lib/desktops.js';
	import { plural } from '#lib/format.js';
	import { DESKTOPS_TABS } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import DesktopTable from './DesktopTable.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import MissingClasses from './MissingClasses.svelte';
	import PoolTable from './PoolTable.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// The Desktops root: every desktop the caller may see, or its pools where
	// it may list no desktop, and the classes they are made from. A kind
	// counts only where the caller may list it: a 0 there would read as none.
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab');
		return DESKTOPS_TABS.find((x) => x.id === t)?.id ?? 'desktops';
	});
	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const may = $derived(data ? desktops.may : null);
	const all = $derived(data ? allDesktops(data) : []);
	const rows = $derived(data ? joinClaims(all, data.claims) : []);
	const listsDesktops = $derived(may?.anywhere('vdiinstances') ?? false);
	const listsPools = $derived(may?.anywhere('vdipools') ?? false);
	const stuck = $derived(data?.pools.filter((p) => poolState(p).missingClass).length ?? 0);

	const tabs = $derived(
		DESKTOPS_TABS.map((t) => ({
			...t,
			count:
				t.id === 'classes'
					? may?.classes
						? data?.classes.length
						: undefined
					: listsDesktops
						? all.length
						: undefined,
		})),
	);
	const context = $derived(
		[
			listsDesktops ? plural(all.length, 'desktop') : '',
			listsPools && data ? plural(data.pools.length, 'pool') : '',
		]
			.filter(Boolean)
			.join(', '),
	);
	const label = $derived(DESKTOPS_TABS.find((t) => t.id === tab)!.label);
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5';
</script>

{#snippet none(line: string)}
	<div class="px-card py-6 text-center text-[13px]">
		<p class="text-ink-muted">{line}</p>
		<p class="text-ink-faint">
			A pool or a desktop declared in a project's repo shows here once applied.
		</p>
	</div>
{/snippet}

<DesktopsFrame
	trail={[{ label: 'Desktops', href: '/desktops' }, { label }]}
	title="Desktops"
	status={stuck ? { tone: 'danger', text: `${plural(stuck, 'pool')} without a class` } : undefined}
	{context}
	{tabs}
	{tab}
>
	{#snippet children(d)}
		{#if tab === 'classes'}
			{#if !may?.classes}
				<UnlistedNote
					lines={['Desktop classes are not shown to you: a platform admin can let you read them.']}
				/>
			{:else}
				<Card title="Desktop classes" meta="Cluster-wide, offered by a platform admin" flush>
					{#if d.classes.length}
						<div class="overflow-x-auto">
							<table class="w-full border-collapse text-[13px]" aria-label="Desktop classes">
								<thead>
									<tr>
										<th scope="col" class="{TH} pl-card">Class</th>
										<th scope="col" class="{TH} pr-card">Image</th>
									</tr>
								</thead>
								<tbody>
									{#each d.classes as c (c.name)}
										<tr class="hover:bg-select-soft">
											<td class="{TD} pl-card font-mono text-xs font-medium">{c.name}</td>
											<td class="{TD} pr-card font-mono text-xs break-all text-ink-muted">
												{c.image}
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{:else}
						<div class="px-card py-6 text-center text-[13px]">
							<p class="text-ink-muted">No desktop class yet.</p>
							<p class="text-ink-faint">A platform admin offers classes for the whole cluster.</p>
						</div>
					{/if}
				</Card>
			{/if}
		{:else}
			<UnlistedNote lines={unlistedLines(d.unlisted, null)} />
			<MissingClasses pools={d.pools} />
			{#if listsDesktops}
				<Card title="All desktops" meta={plural(all.length, 'desktop')} flush>
					<DesktopTable {rows} data={d} namespace pool label="All desktops">
						{#snippet empty()}
							{@render none(
								may?.everywhere('vdiinstances')
									? 'No desktop in your projects yet.'
									: 'No desktop shown to you yet.',
							)}
						{/snippet}
					</DesktopTable>
				</Card>
			{:else if listsPools}
				<Card title="Pools" meta={plural(d.pools.length, 'pool')} flush>
					{#if d.pools.length}
						<PoolTable pools={d.pools} namespace />
					{:else}
						{@render none(
							may?.everywhere('vdipools')
								? 'No pool in your projects yet.'
								: 'No pool shown to you yet.',
						)}
					{/if}
				</Card>
			{/if}
		{/if}
	{/snippet}
</DesktopsFrame>
