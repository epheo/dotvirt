<script lang="ts">
	// The workspace breadcrumb strip: ancestors are links (or callbacks until
	// views are routed), the current object is plain and bold.
	type Crumb = { label: string; href?: string; onclick?: () => void };
	let { trail }: { trail: Crumb[] } = $props();
</script>

<div class="flex items-center gap-2 border-b border-line px-4 py-1.5 text-xs text-ink-muted">
	{#each trail as c, i (i)}
		{#if i > 0}
			<span class="text-line-strong">/</span>
		{/if}
		{#if c.href}
			<a href={c.href} class="text-accent hover:underline">{c.label}</a>
		{:else if c.onclick}
			<button onclick={c.onclick} class="text-accent hover:underline">{c.label}</button>
		{:else if i === trail.length - 1}
			<span class="truncate font-medium text-ink-soft">{c.label}</span>
		{:else}
			<span class="truncate">{c.label}</span>
		{/if}
	{/each}
</div>
