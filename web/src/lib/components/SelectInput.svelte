<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLSelectAttributes } from 'svelte/elements';

	// The canonical modal select, styled to match TextInput (size 'sm' = the
	// compact table/toolbar variant). Options stay the caller's markup so
	// optgroups and dynamic lists need no prop plumbing.
	let {
		value = $bindable(),
		size = 'md',
		class: cls = '',
		children,
		...rest
	}: { value?: string; size?: 'md' | 'sm'; class?: string; children: Snippet } & Omit<
		HTMLSelectAttributes,
		'value' | 'class' | 'size'
	> = $props();
</script>

<select
	bind:value
	class="w-full rounded border border-line-strong {size === 'sm'
		? 'px-2 py-1 text-xs'
		: 'px-2 py-1.5 text-sm'} focus:border-accent/60 disabled:bg-inset-strong disabled:text-ink-faint {cls}"
	{...rest}
>
	{@render children()}
</select>
