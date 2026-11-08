<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';

	// The button shapes in the product, all radius-6 rectangles: primary (blue
	// fill, the one per surface), outline (blue border, a strong second verb),
	// secondary (grey border, the quiet one), ghost (a footer's Cancel/Back),
	// danger (the red-filled destructive confirm), danger-secondary (the quiet
	// entry to a destructive flow: grey border, red ink), link (accent text).
	// href renders an anchor with the same look, for a verb that is a
	// navigation (a forge deep link, a tab). class is for layout only (ml-auto,
	// flex-1, w-full).
	let {
		variant = 'primary',
		size = 'md',
		href,
		class: cls = '',
		children,
		...rest
	}: {
		variant?:
			'primary' | 'outline' | 'secondary' | 'ghost' | 'danger' | 'danger-secondary' | 'link';
		size?: 'md' | 'sm';
		href?: string;
		class?: string;
		children: Snippet;
	} & Omit<HTMLButtonAttributes, 'class'> &
		Pick<HTMLAnchorAttributes, 'target' | 'rel'> = $props();

	const SHAPE = {
		primary:
			'border border-accent bg-accent font-bold text-white hover:border-accent-deep hover:bg-accent-deep disabled:border-line-soft disabled:bg-inset disabled:text-ink-faint',
		outline:
			'border border-accent bg-panel font-medium text-accent-ink hover:bg-select-soft disabled:opacity-50',
		secondary:
			'border border-line bg-panel font-medium text-ink hover:border-ink-faint disabled:opacity-50 disabled:hover:border-line',
		ghost: 'text-ink-soft hover:bg-inset-strong disabled:text-ink-faint',
		danger:
			'border border-danger-fill bg-danger-fill font-bold text-white hover:border-danger-deep hover:bg-danger-deep disabled:opacity-50',
		'danger-secondary':
			'border border-line bg-panel font-medium text-danger-ink hover:border-danger disabled:opacity-50',
		link: 'text-accent-ink hover:underline disabled:opacity-50',
	};
	const BOX = {
		md: 'h-8 justify-center gap-1.5 rounded-md px-3.5 text-sm whitespace-nowrap',
		sm: 'h-7 justify-center gap-1.5 rounded-md px-2.5 text-[13px] whitespace-nowrap',
	};
	const classes = $derived(
		[
			'inline-flex items-center',
			SHAPE[variant],
			variant === 'link' ? `gap-1.5 ${size === 'sm' ? 'text-xs' : ''}` : BOX[size],
			cls,
		].join(' '),
	);
</script>

{#if href}
	<a {href} class={classes} {...rest as HTMLAnchorAttributes}>{@render children()}</a>
{:else}
	<button type="button" class={classes} {...rest}>{@render children()}</button>
{/if}
