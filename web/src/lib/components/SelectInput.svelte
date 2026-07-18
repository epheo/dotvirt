<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLSelectAttributes } from 'svelte/elements';

	// The canonical modal select, styled to match TextInput. Options stay the
	// caller's markup so optgroups and dynamic lists need no prop plumbing.
	let {
		value = $bindable(),
		class: cls = '',
		children,
		...rest
	}: { value?: string; class?: string; children: Snippet } & Omit<
		HTMLSelectAttributes,
		'value' | 'class'
	> = $props();
</script>

<select
	bind:value
	class="w-full rounded border border-line-strong px-2 py-1.5 text-sm focus:border-accent/60 {cls}"
	{...rest}
>
	{@render children()}
</select>
