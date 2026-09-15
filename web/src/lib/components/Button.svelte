<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';

	// The three button shapes in the product: primary (the one pill per
	// surface), secondary (bordered), link (accent text). href renders an
	// anchor with the same look, for a verb that is a navigation (a forge
	// deep link, a tab). class is for layout only (ml-auto, flex-1, w-full).
	let {
		variant = 'primary',
		size = 'md',
		href,
		class: cls = '',
		children,
		...rest
	}: {
		variant?: 'primary' | 'secondary' | 'link';
		size?: 'md' | 'sm';
		href?: string;
		class?: string;
		children: Snippet;
	} & Omit<HTMLButtonAttributes, 'class'> &
		Pick<HTMLAnchorAttributes, 'target' | 'rel'> = $props();

	const SHAPE = {
		primary:
			'rounded-full bg-accent font-medium text-white hover:bg-accent-hover disabled:bg-line-strong',
		secondary:
			'rounded border border-line-strong bg-panel text-ink-soft hover:bg-inset disabled:opacity-50',
		link: 'text-accent-ink hover:underline disabled:opacity-50',
	};
	const PAD = { md: 'px-4 py-1.5 text-sm', sm: 'px-3 py-1 text-xs' };
	const classes = $derived(
		[
			'inline-flex items-center gap-1.5',
			SHAPE[variant],
			variant === 'link' ? (size === 'sm' ? 'text-xs' : '') : PAD[size],
			cls,
		].join(' '),
	);
</script>

{#if href}
	<a {href} class={classes} {...rest as HTMLAnchorAttributes}>{@render children()}</a>
{:else}
	<button type="button" class={classes} {...rest}>{@render children()}</button>
{/if}
