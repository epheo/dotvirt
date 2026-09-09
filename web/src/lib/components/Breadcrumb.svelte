<script lang="ts">
	import { page } from '$app/state';
	import { keepTab } from '$lib/nav';

	// The workspace breadcrumb strip: ancestors navigate (href, or a callback
	// where the host owns the transition), the current object is plain and bold.
	// Walking up keeps the tab in view, like the tree (see keepTab).
	type Crumb = { label: string; href?: string; onclick?: () => void };
	let { trail }: { trail: Crumb[] } = $props();
	const tab = $derived(page.url.searchParams.get('tab'));
</script>

<div class="flex items-center gap-2 border-b border-line px-4 py-1.5 text-xs text-ink-muted">
	{#each trail as c, i (i)}
		{#if i > 0}
			<span class="text-line-strong">/</span>
		{/if}
		{#if c.href}
			<a href={keepTab(c.href, tab)} class="text-accent hover:underline">{c.label}</a>
		{:else if c.onclick}
			<button onclick={c.onclick} class="text-accent hover:underline">{c.label}</button>
		{:else if i === trail.length - 1}
			<span class="truncate font-medium text-ink-soft">{c.label}</span>
		{:else}
			<span class="truncate">{c.label}</span>
		{/if}
	{/each}
</div>
