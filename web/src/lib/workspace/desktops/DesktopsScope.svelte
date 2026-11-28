<script lang="ts">
	import type { Scope } from '#lib/lenses.js';
	import { joinClaims, poolState, unlistedLines } from '#lib/desktops.js';
	import { plural } from '#lib/format.js';
	import { hrefForScope, trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
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
	const project = $derived(desktops.projects.find((p) => p.name === scope.project));
	const known = $derived(
		!!project && (!one || project.namespaces.some((n) => n.namespace === one)),
	);
	// The namespaces in scope the answer covers: a project without a repo has none.
	const namespaces = $derived(
		new Set(
			(one ? [one] : (project?.namespaces.map((n) => n.namespace) ?? [])).filter((ns) =>
				desktops.covered.has(ns),
			),
		),
	);

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const may = $derived(data ? desktops.may : null);
	const pools = $derived(data?.pools.filter((p) => namespaces.has(p.namespace)) ?? []);
	const loose = $derived(data?.desktops.filter((d) => namespaces.has(d.namespace)) ?? []);
	const byOne = $derived(loose.filter((d) => !d.pool));
	// The desktops of pools the caller is not shown: still desktops it may see.
	const orphans = $derived(loose.filter((d) => d.pool));
	const total = $derived(pools.reduce((n, p) => n + p.desktops.length, 0) + loose.length);
	const stuck = $derived(pools.filter((p) => poolState(p).missingClass).length);
	const lists = (r: string) => may?.anywhere(r, namespaces) ?? false;

	const title = $derived(one || desktops.title(scope.project));
	// A kind counts only where the caller may list it somewhere in scope: a 0
	// would read as none.
	const context = $derived.by(() => {
		if (!data) return '';
		const size = [
			lists('vdipools') ? plural(pools.length, 'pool') : '',
			lists('vdiinstances') ? plural(total, 'desktop') : '',
		]
			.filter(Boolean)
			.join(', ');
		return one ? [size, `in ${desktops.title(scope.project)}`].filter(Boolean).join(' ') : size;
	});
	const lines = $derived(data ? unlistedLines(data.unlisted, namespaces, !!one) : []);
</script>

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => desktops.title(p), 'desktops')}
	{title}
	status={stuck ? { tone: 'danger', text: `${plural(stuck, 'pool')} without a class` } : undefined}
	{context}
>
	{#snippet children(d)}
		{#if !known}
			<p class="text-[13px] text-ink-muted">
				{one
					? `No namespace ${one} in ${desktops.title(scope.project)} is shown to you.`
					: `No project ${scope.project} is shown to you.`}
				<a class="text-accent-ink hover:underline" href="/desktops">See all desktops</a>
			</p>
		{:else if namespaces.size === 0}
			<p class="text-[13px] text-ink-muted">
				Desktops are read in projects with a repo, and {desktops.title(scope.project)} has none.
				<a
					class="text-accent-ink hover:underline"
					href={hrefForScope({ kind: 'project', project: scope.project })}
				>
					Its project page says how to give it one
				</a>
			</p>
		{:else}
			<UnlistedNote {lines} />
			<MissingClasses {pools} />
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
					<PoolTable {pools} namespace={!one} />
				</Card>
			{/if}
			{#if byOne.length}
				<Card title="Desktops made one by one" meta={plural(byOne.length, 'desktop')} flush>
					<DesktopTable
						rows={joinClaims(byOne, d.claims)}
						data={d}
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
						namespace={!one}
						pool
						label="Desktops of pools not shown to you"
					/>
				</Card>
			{/if}
		{/if}
	{/snippet}
</DesktopsFrame>
