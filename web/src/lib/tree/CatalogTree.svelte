<script lang="ts">
	import { page } from '$app/state';
	import { BookCopy, Cpu, Database, HardDrive, Network, SlidersHorizontal } from 'lucide-svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';

	// The Catalog tree (Content Libraries analog): the template library first —
	// the deployable content — then the read-only platform object kinds the New
	// VM wizard consumes.
	const KINDS = [
		{ id: 'templates', label: 'VM Templates' },
		{ id: 'images', label: 'Boot images' },
		{ id: 'instancetypes', label: 'Instance types' },
		{ id: 'preferences', label: 'Preferences' },
		{ id: 'networks', label: 'Networks' },
		{ id: 'storage', label: 'Storage classes' },
	];
	const kind = $derived(page.url.searchParams.get('kind') ?? 'templates');
	const onCatalog = $derived(page.url.pathname === '/catalog');
</script>

<div class="select-none text-[13px]">
	{#each KINDS as k (k.id)}
		<TreeRow active={onCatalog && kind === k.id} alignChevron href="/catalog?kind={k.id}">
			{#snippet icon()}
				{#if k.id === 'templates'}<BookCopy size={14} class="text-ink-faint" />
				{:else if k.id === 'images'}<HardDrive size={14} class="text-ink-faint" />
				{:else if k.id === 'instancetypes'}<Cpu size={14} class="text-ink-faint" />
				{:else if k.id === 'preferences'}<SlidersHorizontal size={14} class="text-ink-faint" />
				{:else if k.id === 'networks'}<Network size={14} class="text-ink-faint" />
				{:else}<Database size={14} class="text-ink-faint" />{/if}
			{/snippet}
			<span class="truncate font-semibold text-ink-soft">{k.label}</span>
		</TreeRow>
	{/each}
</div>
