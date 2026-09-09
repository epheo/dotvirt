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
	} from 'lucide-svelte';
	import {
		CATALOG_KINDS,
		catalogHref,
		catalogKind,
		catalogRows,
		type CatalogKind,
	} from '$lib/catalog';
	import { catalog } from '$lib/state/catalog.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';

	// The Catalog tree (Content Libraries analog): one group per kind - the
	// template library first, the deployable content - with the kind's items
	// as rows, like every other tree lists objects under its groups. Clicking
	// an item opens it in the workspace's detail pane.
	const kind = $derived(catalogKind(page.url.searchParams.get('kind')));
	const item = $derived(page.url.searchParams.get('item'));
	const onCatalog = $derived(page.url.pathname === '/catalog');

	$effect(() => {
		catalog.load();
	});

	// Groups default-open only for the kind in view; an explicit toggle wins.
	let collapsed = $state<Record<string, boolean>>({});
	const open = (k: CatalogKind) => (collapsed[k] === undefined ? k === kind : !collapsed[k]);
	const toggle = (k: CatalogKind) => (collapsed[k] = open(k));

	const rowsOf = (k: CatalogKind) => catalogRows(k, catalog.templates, inventory.options) ?? [];
</script>

<div class="select-none text-[13px]">
	{#each CATALOG_KINDS as k (k.id)}
		{@const rows = rowsOf(k.id)}
		<div>
			<TreeRow
				active={onCatalog && kind === k.id && !item}
				expanded={open(k.id)}
				ontoggle={() => toggle(k.id)}
				href={catalogHref(k.id)}
			>
				{#snippet icon()}
					{#if k.id === 'templates'}<BookCopy size={14} class="shrink-0 text-side-dim" />
					{:else if k.id === 'images'}<HardDrive size={14} class="shrink-0 text-side-dim" />
					{:else if k.id === 'instancetypes'}<Cpu size={14} class="shrink-0 text-side-dim" />
					{:else if k.id === 'preferences'}<SlidersHorizontal
							size={14}
							class="shrink-0 text-side-dim"
						/>
					{:else if k.id === 'networks'}<Network size={14} class="shrink-0 text-side-dim" />
					{:else}<Database size={14} class="shrink-0 text-side-dim" />{/if}
				{/snippet}
				<span class="truncate font-semibold text-side-ink">{k.label}</span>
				{#snippet trailing()}
					<span class="text-xs text-side-dim">{rows.length}</span>
				{/snippet}
			</TreeRow>
			{#if open(k.id)}
				{#each rows as r (r.key)}
					<TreeRow
						indent={2}
						active={onCatalog && kind === k.id && item === r.key}
						href={catalogHref(k.id, r.key)}
					>
						<span class="truncate text-side-ink">{r.title}</span>
						{#snippet trailing()}
							{#if r.template?.error}
								<TriangleAlert size={12} class="text-warn" />
							{/if}
						{/snippet}
					</TreeRow>
				{/each}
			{/if}
		</div>
	{/each}
</div>
