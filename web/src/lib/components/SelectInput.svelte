<script lang="ts" generics="V extends string | number">
	import type { Snippet } from 'svelte';
	import type { HTMLSelectAttributes } from 'svelte/elements';

	// The canonical modal select, styled to match TextInput (size 'sm' = the
	// compact table/toolbar variant; width = the width utility, full by
	// default). Options stay the caller's markup so optgroups and dynamic
	// lists need no prop plumbing.
	let {
		value = $bindable(),
		size = 'md',
		width = 'w-full',
		class: cls = '',
		children,
		...rest
	}: {
		value?: V;
		size?: 'md' | 'sm';
		width?: string;
		class?: string;
		children: Snippet;
	} & Omit<HTMLSelectAttributes, 'value' | 'class' | 'size' | 'width'> = $props();
</script>

<select
	bind:value
	class="{width} rounded border border-line-strong {size === 'sm'
		? 'px-2 py-1 text-xs'
		: 'px-2 py-1.5 text-sm'} focus:border-accent/60 disabled:bg-inset-strong disabled:text-ink-faint {cls}"
	{...rest}
>
	{@render children()}
</select>
