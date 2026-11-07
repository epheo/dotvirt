<script lang="ts">
	import { page } from '$app/state';
	import { keepTab } from '#lib/nav.js';

	// Where the page sits: ancestors are accent links (href, or a callback where
	// the host owns the transition), the current page is plain, "/" between.
	// Walking up keeps the tab in view, like the tree (see keepTab). class is
	// for placement only (padding, a strip border).
	type Crumb = { label: string; href?: string; onclick?: () => void };
	let { trail, class: cls = '' }: { trail: Crumb[]; class?: string } = $props();
	const tab = $derived(page.url.searchParams.get('tab'));
</script>

<nav
	aria-label="Breadcrumb"
	class="flex flex-wrap items-center gap-1.5 text-[13px] text-ink-muted {cls}"
>
	{#each trail as c, i (i)}
		{#if i > 0}
			<span aria-hidden="true">/</span>
		{/if}
		{#if c.href}
			<a href={keepTab(c.href, tab)} class="text-accent-ink hover:underline">{c.label}</a>
		{:else if c.onclick}
			<button onclick={c.onclick} class="text-accent-ink hover:underline">{c.label}</button>
		{:else}
			<span class="truncate" aria-current={i === trail.length - 1 ? 'page' : undefined}
				>{c.label}</span
			>
		{/if}
	{/each}
</nav>
