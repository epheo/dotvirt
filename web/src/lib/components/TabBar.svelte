<script lang="ts">
	// The one tab strip. `underline` is the page idiom: the selected tab bold
	// over a 3px accent bar, counts as muted text after the label ("Networks
	// 131"), the strip's own hairline below. `chips` is the compact idiom for
	// drawers and the task dock, counts in a badge. Tabs render as links when
	// the host passes an `href` builder, else as buttons driving `onchange`.
	type Tab = {
		id: string;
		label: string;
		count?: number;
		countTone?: 'neutral' | 'warn';
	};
	let {
		tabs,
		active,
		variant = 'underline',
		class: cls = '',
		href = undefined,
		onchange = undefined,
	}: {
		tabs: Tab[];
		active: string;
		variant?: 'underline' | 'chips';
		class?: string;
		href?: (id: string) => string;
		onchange?: (id: string) => void;
	} = $props();

	const itemClass = (t: Tab) =>
		variant === 'underline'
			? `border-b-[3px] px-3.5 py-2.5 whitespace-nowrap ${
					active === t.id
						? 'border-accent font-bold text-ink'
						: 'border-transparent text-ink-muted hover:text-ink'
				}`
			: `rounded px-2 py-0.5 text-xs ${
					active === t.id
						? 'bg-select font-medium text-accent-ink'
						: 'text-ink-muted hover:bg-inset hover:text-ink-soft'
				}`;
</script>

{#snippet count(t: Tab)}
	{#if t.count !== undefined}
		{#if variant === 'underline'}
			<span
				class="ml-0.5 text-[13px] font-normal {t.countTone === 'warn'
					? 'text-warn-ink'
					: 'text-ink-faint'}">{t.count.toLocaleString('en-US')}</span
			>
		{:else}
			<span
				class="ml-0.5 rounded-full px-1.5 text-[11px] {t.countTone === 'warn'
					? 'bg-warn/30 font-medium text-warn-ink'
					: 'bg-line-strong text-ink-soft'}">{t.count.toLocaleString('en-US')}</span
			>
		{/if}
	{/if}
{/snippet}

<nav
	class="flex {variant === 'underline'
		? 'gap-1 overflow-x-auto border-b border-line text-sm'
		: 'flex-wrap gap-1'} {cls}"
>
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
