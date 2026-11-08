<script lang="ts">
	// The one tab strip: the selected tab bold over a 3px accent bar, counts as
	// muted text after the label ("Networks 131"), the strip's own hairline
	// below. Tabs render as links when the host passes an `href` builder, else
	// as buttons driving `onchange`.
	type Tab = {
		id: string;
		label: string;
		count?: number;
		countTone?: 'neutral' | 'warn';
	};
	let {
		tabs,
		active,
		class: cls = '',
		href = undefined,
		onchange = undefined,
	}: {
		tabs: Tab[];
		active: string;
		class?: string;
		href?: (id: string) => string;
		onchange?: (id: string) => void;
	} = $props();

	const itemClass = (t: Tab) =>
		`border-b-[3px] px-3.5 py-2.5 whitespace-nowrap ${
			active === t.id
				? 'border-accent font-bold text-ink'
				: 'border-transparent text-ink-muted hover:text-ink'
		}`;
</script>

{#snippet count(t: Tab)}
	{#if t.count !== undefined}
		<span
			class="ml-0.5 text-[13px] font-normal {t.countTone === 'warn'
				? 'text-warn-ink'
				: 'text-ink-faint'}">{t.count.toLocaleString('en-US')}</span
		>
	{/if}
{/snippet}

<nav class="flex gap-1 overflow-x-auto border-b border-line text-sm {cls}">
	{#each tabs as t (t.id)}
		{#if href}
			<a
				href={href(t.id)}
				data-sveltekit-replacestate
				aria-current={active === t.id ? 'page' : undefined}
				class={itemClass(t)}
			>
				{t.label}
				{@render count(t)}
			</a>
		{:else}
			<button
				onclick={() => onchange?.(t.id)}
				aria-current={active === t.id ? 'true' : undefined}
				class={itemClass(t)}
			>
				{t.label}
				{@render count(t)}
			</button>
		{/if}
	{/each}
</nav>
