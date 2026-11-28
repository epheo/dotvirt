<script lang="ts">
	import { page } from '$app/state';
	import { Folder, Layers, Users } from '@lucide/svelte';
	import {
		allDesktops,
		byProject,
		count,
		desktopState,
		poolState,
		type DesktopsNamespace,
		type DesktopsProject,
	} from '#lib/desktops.js';
	import { hrefForScope, scopeFromPath } from '#lib/nav.js';
	import { LEAF_LIMIT, leafWindow, matches, SMALL_FLEET } from '#lib/rail.js';
	import { desktops, nothingReadable } from '#lib/state/desktops.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import StatusDot from '#lib/components/StatusDot.svelte';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import RailFilter from './RailFilter.svelte';
	import RailMore from './RailMore.svelte';

	// The Desktops tree: All desktops, Desktop classes, then projects,
	// namespaces, and in each its pools and the desktops made one by one. A
	// pool's desktops are rows of its page, never rows here. Highlights derive
	// from the URL.
	const scope = $derived(scopeFromPath(page.url.pathname));
	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const groups = $derived(data ? byProject(data, inventory.projects) : []);
	const total = $derived(data ? allDesktops(data).length : 0);
	const small = $derived(total <= SMALL_FLEET);
	const fmt = (n: number) => n.toLocaleString('en-US');
	const onClasses = $derived(
		page.url.pathname === '/desktops' && page.url.searchParams.get('tab') === 'classes',
	);

	let query = $state('');
	const nsShown = (ns: DesktopsNamespace) =>
		matches(query, ns.namespace, ...ns.pools.map((p) => p.name), ...ns.desktops.map((d) => d.name));
	const shown = $derived(
		groups
			.map((g) =>
				matches(query, g.project, inventory.title(g.project))
					? g
					: { ...g, namespaces: g.namespaces.filter(nsShown) },
			)
			.filter((g) => g.namespaces.length),
	);

	const inView = $derived(
		scope.kind === 'project' ||
			scope.kind === 'namespace' ||
			scope.kind === 'pool' ||
			scope.kind === 'desktop'
			? scope
			: null,
	);
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.desktops', {});
	const isOpen = (id: string, byDefault: boolean) => {
		const v = collapsed.value[id];
		return v === undefined ? byDefault || query.trim() !== '' : !v;
	};
	const setOpen = (id: string, open: boolean) =>
		(collapsed.value = { ...collapsed.value, [id]: !open });
	const projectOpen = (g: DesktopsProject) =>
		isOpen(`p:${g.project}`, small || inView?.project === g.project);
	const nsOpen = (g: DesktopsProject, ns: DesktopsNamespace) =>
		isOpen(
			`n:${g.project}/${ns.namespace}`,
			small ||
				(inView?.project === g.project &&
					'namespace' in inView &&
					inView.namespace === ns.namespace),
		);
	const at = (kind: 'pool' | 'desktop', namespace: string, name: string) =>
		scope.kind === kind &&
		scope.namespace === namespace &&
		(scope.kind === 'pool' ? scope.pool : scope.desktop) === name;
	const KIND_LINE: Record<string, string> = {
		vdipools: 'Pools not shown to you',
		vdiinstances: 'Desktops not shown to you',
	};
</script>

<div class="flex flex-col gap-0.5 px-2.5 pb-3 text-[13px] select-none">
	<RailFilter bind:value={query} label="Filter desktops" placeholder="Filter pools and desktops" />

	<!-- A row keeps the tab in view (keepTab), so All desktops names its tab to leave the classes. -->
	<TreeRow
		active={page.url.pathname === '/desktops' && !onClasses}
		href={onClasses ? '/desktops?tab=desktops' : '/desktops'}
	>
		<span class="truncate text-side-ink">All desktops</span>
		{#snippet trailing()}{#if data}<span class="text-xs text-side-dim">
					{fmt(total)}
				</span>{/if}{/snippet}
	</TreeRow>
	<TreeRow active={onClasses} href="/desktops?tab=classes">
		<span class="truncate text-side-ink">Desktop classes</span>
		{#snippet trailing()}{#if data}<span class="text-xs text-side-dim">
					{fmt(data.classes.length)}
				</span>{/if}{/snippet}
	</TreeRow>

	{#if !desktops.value}
		<div class="p-1"><Skeleton rows={3} class="h-5" bg="bg-side-hover" /></div>
	{:else if shown.length > 0}
		<div class="px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase">
			Projects
		</div>
	{/if}

	{#each shown as g (g.project)}
		{@const open = projectOpen(g)}
		<TreeRow
			active={scope.kind === 'project' && scope.project === g.project}
			expanded={open}
			ontoggle={() => setOpen(`p:${g.project}`, !open)}
			href={hrefForScope({ kind: 'project', project: g.project }, 'desktops')}
		>
			{#snippet icon()}<Folder size={14} class="shrink-0 text-side-dim" />{/snippet}
			<span class="truncate {open ? 'text-side-ink' : ''}">{inventory.title(g.project)}</span>
			{#snippet trailing()}
				{#if g.missingClass}
					<StatusDot
						tone="danger"
						size="xs"
						title="A pool here names a class that does not exist"
					/>
				{/if}
				{#if g.count !== null}<span class="text-xs text-side-dim">{fmt(g.count)}</span>{/if}
			{/snippet}
		</TreeRow>

		{#if open}
			{#each g.namespaces as ns (ns.namespace)}
				{@const nsIsOpen = nsOpen(g, ns)}
				{@const nsHref = hrefForScope(
					{ kind: 'namespace', project: g.project, namespace: ns.namespace },
					'desktops',
				)}
				{@const leaves = leafWindow(ns.desktops, LEAF_LIMIT, (d) =>
					at('desktop', d.namespace, d.name),
				)}
				<TreeRow
					indent={1}
					active={scope.kind === 'namespace' &&
						scope.project === g.project &&
						scope.namespace === ns.namespace}
					expanded={nsIsOpen}
					ontoggle={() => setOpen(`n:${g.project}/${ns.namespace}`, !nsIsOpen)}
					href={nsHref}
				>
					{#snippet icon()}<Layers size={13} class="shrink-0 text-side-dim" />{/snippet}
					<span class="truncate {nsIsOpen ? 'text-side-ink' : ''}">{ns.namespace}</span>
					{#snippet trailing()}
						{#if ns.missingClass}
							<StatusDot
								tone="danger"
								size="xs"
								title="A pool here names a class that does not exist"
							/>
						{/if}
						{#if ns.count !== null}<span class="text-xs text-side-dim">{fmt(ns.count)}</span>{/if}
					{/snippet}
				</TreeRow>

				{#if nsIsOpen}
					{#each ns.pools as p (p.name)}
						{@const st = poolState(p)}
						<TreeRow
							indent={3}
							active={at('pool', p.namespace, p.name)}
							href={hrefForScope({
								kind: 'pool',
								project: g.project,
								namespace: p.namespace,
								pool: p.name,
							})}
						>
							{#snippet icon()}<Users size={13} class="shrink-0 text-side-dim" />{/snippet}
							<span class="truncate text-side-ink">{p.name}</span>
							{#snippet trailing()}
								{#if st.tone === 'danger' || st.tone === 'warn'}
									<StatusDot tone={st.tone} size="xs" title={st.label} />
								{/if}
								<span class="text-xs text-side-dim" title="Seats held">
									{fmt(count(p, 'held'))}/{fmt(p.seats)}
								</span>
							{/snippet}
						</TreeRow>
					{/each}
					{#each leaves.shown as d (d.name)}
						<TreeRow
							indent={3}
							active={at('desktop', d.namespace, d.name)}
							href={hrefForScope({
								kind: 'desktop',
								project: g.project,
								namespace: d.namespace,
								desktop: d.name,
							})}
						>
							{#snippet icon()}<StatusDot tone={desktopState(d).tone} />{/snippet}
							<span class="truncate text-side-ink">{d.name}</span>
						</TreeRow>
					{/each}
					{#if leaves.more > 0}
						<RailMore href={nsHref} indent={3}>{fmt(leaves.more)} more desktops</RailMore>
					{/if}
					{#each ns.unlisted.filter((r) => KIND_LINE[r]) as r (r)}
						<div class="py-1 pl-12 text-xs text-side-dim italic">{KIND_LINE[r]}</div>
					{/each}
				{/if}
			{/each}
		{/if}
	{/each}

	{#if query.trim() && data && shown.length === 0}
		<div class="px-2 py-3 text-xs text-side-dim">No project, pool or desktop matches.</div>
	{:else if data && groups.length === 0}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No desktop in your projects yet.</div>
	{:else if nothingReadable()}
		<div class="px-2 py-4 text-center text-xs text-side-dim">No desktop is shown to you.</div>
	{/if}
</div>
