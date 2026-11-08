<script lang="ts">
	import { page } from '$app/state';
	import {
		BookCopy,
		Cpu,
		Database,
		HardDrive,
		Network,
		SlidersHorizontal,
		TriangleAlert,
	} from '@lucide/svelte';
	import {
		CATALOG_KINDS,
		catalogHref,
		catalogKind,
		catalogRows,
		libraryLabel,
		type CatalogKind,
	} from '#lib/catalog.js';
	import { LEAF_LIMIT, leafWindow, matches } from '#lib/rail.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import RailFilter from './RailFilter.svelte';
	import RailMore from './RailMore.svelte';

	// The Catalog tree: one group per kind, templates (kept in git) above the
	// platform's kinds, each listing its first items with "N more" opening the
	// kind's tab. An item opens beside its table in the workspace.
	const kind = $derived(catalogKind(page.url.searchParams.get('kind')));
	const item = $derived(page.url.searchParams.get('item'));
	const onCatalog = $derived(page.url.pathname === '/catalog');
	const fmt = (n: number) => n.toLocaleString('en-US');

	$effect(() => {
		catalog.load();
	});

	let query = $state('');
	const filtering = $derived(query.trim() !== '');
	const all = $derived(
		CATALOG_KINDS.map((k) => ({
			...k,
			rows: catalogRows(k.id, catalog.templates, inventory.options, inventory.networks) ?? [],
		})),
	);
	const total = $derived(all.reduce((n, k) => n + k.rows.length, 0));
	const shown = $derived(
		all
			.map((k) => ({
				...k,
				rows: k.rows.filter((r) =>
					matches(query, r.title, r.template ? libraryLabel(r.template.library) : ''),
				),
			}))
			.filter((k) => !filtering || k.rows.length > 0),
	);
	const firstPlatform = $derived(shown.find((k) => k.id !== 'templates')?.id);

	// Unset means the default: open for the kind in view, or when the filter
	// matched inside. An explicit toggle wins and persists.
	const collapsed = persisted<Record<string, boolean>>('dotvirt.tree.catalog', {});
	const isOpen = (k: CatalogKind) => {
		const v = collapsed.value[k];
		return v === undefined ? filtering || (onCatalog && k === kind) : !v;
	};
	const setOpen = (k: CatalogKind, open: boolean) =>
		(collapsed.value = { ...collapsed.value, [k]: !open });

	const ICON = {
		templates: BookCopy,
		images: HardDrive,
		instancetypes: Cpu,
		preferences: SlidersHorizontal,
		networks: Network,
		storage: Database,
	};
	const NOUN: Record<CatalogKind, string> = {
		templates: 'templates',
		images: 'images',
		instancetypes: 'instance types',
		preferences: 'preferences',
		networks: 'networks',
		storage: 'classes',
	};
	const HEAD = 'px-2 pt-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase';
</script>

<div class="flex flex-col gap-0.5 px-2.5 pb-3 text-[13px] select-none">
	<RailFilter
		bind:value={query}
		label="Filter the catalog"
		placeholder="Filter {fmt(total)} items"
	/>

	{#each shown as k (k.id)}
		{@const open = isOpen(k.id)}
		{@const leaves = leafWindow(
			k.rows,
			LEAF_LIMIT,
			(r) => onCatalog && kind === k.id && r.key === item,
		)}
		{@const Icon = ICON[k.id]}
		{#if k.id === 'templates'}
			<div class={HEAD}>Library</div>
		{:else if k.id === firstPlatform}
			<div class={HEAD}>Platform</div>
		{/if}
		<TreeRow
			active={onCatalog && kind === k.id && !item}
			expanded={open}
			ontoggle={() => setOpen(k.id, !open)}
			href={catalogHref(k.id)}
		>
			{#snippet icon()}<Icon size={14} class="shrink-0 text-side-dim" />{/snippet}
			<span class="truncate {open ? 'text-side-ink' : ''}">{k.label}</span>
			{#snippet trailing()}
				{#if k.rows.some((r) => r.template?.error)}
					<TriangleAlert size={12} class="text-warn" aria-label="A template does not parse" />
				{/if}
				<span class="text-xs text-side-dim">{fmt(k.rows.length)}</span>
			{/snippet}
		</TreeRow>
		{#if open}
			{#each leaves.shown as r (r.key)}
				<TreeRow
					indent={2}
					active={onCatalog && kind === k.id && item === r.key}
					href={catalogHref(k.id, r.key)}
				>
					<span class="truncate text-side-ink">{r.title}</span>
					<!-- Libraries may carry the same template name; the library tells them apart. -->
					{#snippet trailing()}
						{#if r.template?.error}
							<TriangleAlert size={12} class="text-warn" aria-label="Does not parse" />
						{/if}
						{#if r.template}
							<span class="max-w-24 truncate text-xs text-side-dim"
								>{r.template.library === 'platform' ? 'shared' : r.template.library}</span
							>
						{/if}
					{/snippet}
				</TreeRow>
			{/each}
			{#if leaves.more > 0}
				<RailMore href={catalogHref(k.id)}>{fmt(leaves.more)} more {NOUN[k.id]}</RailMore>
			{/if}
		{/if}
	{/each}

	{#if filtering && shown.length === 0}
		<div class="px-2 py-3 text-xs text-side-dim">Nothing in the catalog matches.</div>
	{/if}
</div>
