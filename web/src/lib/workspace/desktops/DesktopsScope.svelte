<script lang="ts">
	import type { Scope } from '#lib/lenses.js';
	import { joinClaims, listed, poolState, projectOf, unlistedLines } from '#lib/desktops.js';
	import { plural } from '#lib/format.js';
	import { trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import DesktopTable from './DesktopTable.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import MissingClasses from './MissingClasses.svelte';
	import PoolTable from './PoolTable.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// A project's or a namespace's desktops: its pools, and the desktops made
	// one by one there. A pool's desktops are on its page.
	let { scope }: { scope: Extract<Scope, { kind: 'project' | 'namespace' }> } = $props();

	const one = $derived(scope.kind === 'namespace' ? scope.namespace : '');
	const project = $derived(inventory.projects.find((p) => p.name === scope.project));
	const nsKey = $derived(
		one || (project?.namespaces.map((n) => n.namespace) ?? []).sort().join('\0'),
	);
	const namespaces = $derived(new Set(nsKey ? nsKey.split('\0') : []));
	const owners = $derived(projectOf(inventory.projects));

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const pools = $derived(data?.pools.filter((p) => namespaces.has(p.namespace)) ?? []);
	const loose = $derived(data?.desktops.filter((d) => namespaces.has(d.namespace)) ?? []);
	const byOne = $derived(loose.filter((d) => !d.pool));
	// The desktops of pools the caller is not shown: still desktops it may see.
	const orphans = $derived(loose.filter((d) => d.pool));
	const total = $derived(pools.reduce((n, p) => n + p.desktops.length, 0) + loose.length);
	const stuck = $derived(pools.filter((p) => poolState(p).missingClass).length);

	const title = $derived(one || inventory.title(scope.project));
	// A kind counts only where the caller may list it somewhere in scope: a 0
	// would read as none.
	const context = $derived.by(() => {
		if (!data) return '';
		const anywhere = (r: string) => [...namespaces].some((ns) => listed(data, ns, r));
		const size = [
			anywhere('vdipools') ? plural(pools.length, 'pool') : '',
			anywhere('vdiinstances') ? plural(total, 'desktop') : '',
		]
			.filter(Boolean)
			.join(', ');
		return one ? [size, `in ${inventory.title(scope.project)}`].filter(Boolean).join(' ') : size;
	});
	const lines = $derived(data ? unlistedLines(data.unlisted, namespaces, !!one) : []);
</script>

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => inventory.title(p), 'desktops')}
	{title}
	status={stuck ? { tone: 'danger', text: `${plural(stuck, 'pool')} without a class` } : undefined}
	{context}
>
	{#snippet children(d)}
		<UnlistedNote {lines} />
		<MissingClasses {pools} projectOf={owners} />
		{#if pools.length === 0 && loose.length === 0 && lines.length === 0}
			<div class="rounded-lg border border-line bg-panel px-card py-6 text-center text-[13px]">
				<p class="text-ink-muted">No desktop here yet.</p>
				<p class="text-ink-faint">
					A pool or a desktop declared in the project's repo shows here once applied.
				</p>
			</div>
		{/if}
		{#if pools.length}
			<Card title="Pools" meta={plural(pools.length, 'pool')} flush>
				<PoolTable {pools} projectOf={owners} namespace={!one} />
			</Card>
		{/if}
		{#if byOne.length}
			<Card title="Desktops made one by one" meta={plural(byOne.length, 'desktop')} flush>
				<DesktopTable
					rows={joinClaims(byOne, d.claims)}
					data={d}
					projectOf={owners}
					namespace={!one}
					label="Desktops made one by one"
				/>
			</Card>
		{/if}
		{#if orphans.length}
			<Card
				title="Desktops of pools not shown to you"
				meta={plural(orphans.length, 'desktop')}
				flush
			>
				<DesktopTable
					rows={joinClaims(orphans, d.claims)}
					data={d}
					projectOf={owners}
					namespace={!one}
					pool
					label="Desktops of pools not shown to you"
				/>
			</Card>
		{/if}
	{/snippet}
</DesktopsFrame>
