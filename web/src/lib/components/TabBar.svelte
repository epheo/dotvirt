<script lang="ts">
	// The one tab strip. `underline` is the workspace idiom (container + VM object
	// pages); `chips` is the compact idiom for drawers and the task dock. Tabs
	// render as links when the host passes an `href` builder, else as buttons
	// driving `onchange`.
	type Tab = {
		id: string;
		label: string;
		title?: string;
		disabled?: boolean;
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
			? `border-b-2 px-3 py-1.5 ${
					active === t.id
						? 'border-accent text-accent-ink'
						: 'border-transparent text-ink-muted hover:text-ink-soft'
				}`
			: `rounded px-2 py-0.5 text-xs ${
					active === t.id
						? 'bg-select font-medium text-accent-ink'
						: 'text-ink-muted hover:bg-inset hover:text-ink-soft'
				}`;
</script>

<nav class="flex {variant === 'underline' ? 'gap-1 text-sm' : 'flex-wrap gap-1'} {cls}">
	{#each tabs as t (t.id)}
		{#if href && !t.disabled}
			<a href={href(t.id)} data-sveltekit-replacestate title={t.title} class={itemClass(t)}>
				{t.label}
			</a>
		{:else}
			<button
				onclick={() => onchange?.(t.id)}
				disabled={t.disabled}
				title={t.title}
				class="{itemClass(t)} disabled:cursor-not-allowed disabled:text-ink-faint"
			>
				{t.label}
				{#if t.count !== undefined}
					<span
						class="ml-0.5 rounded-full px-1.5 text-[11px] {t.countTone === 'warn'
							? 'bg-warn/30 font-medium text-warn-ink'
							: 'bg-line-strong text-ink-soft'}">{t.count}</span
					>
				{/if}
			</button>
		{/if}
	{/each}
</nav>
